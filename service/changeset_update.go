package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *ChangesetService) UpdateChangeset(ctx context.Context, req *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := requireEditBlobsAccessible(ctx, s.Blobs, slice, req.FileEdits); err != nil {
		return nil, err
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		baseCommitID = cs.BaseCommitId
	}
	baseKind, basePatchsetID, baseTreeID, err := s.patchsetBaseSource(ctx, cs, req, baseCommitID)
	if err != nil {
		return nil, err
	}
	validation, err := s.validator.validateFileEdits(ctx, slice, baseCommitID, baseTreeID, req.FileEdits, true)
	if err != nil {
		return nil, err
	}
	patchset := &corev1.Patchset{
		BaseCommitId:          baseCommitID,
		Author:                subjectID,
		ChangedPaths:          validation.AffectedPaths,
		FileEdits:             req.FileEdits,
		Coverage:              validation.Coverage,
		SubmitRequirements:    validation.SubmitRequirements,
		PathBases:             validation.PathBases,
		ReadSet:               validation.ReadSet,
		WriteSet:              validation.WriteSet,
		Conflicts:             req.Conflicts,
		Kind:                  req.PatchsetKind,
		BaseKind:              baseKind,
		BasePatchsetId:        basePatchsetID,
		BaseTreeId:            baseTreeID,
		StackParentPatchsetId: basePatchsetID,
	}
	if err := s.applyAuthoringConversation(ctx, patchset, slice.Id, req.ConversationId); err != nil {
		return nil, err
	}
	priorPatchsetID := cs.CurrentPatchsetId
	patchset, err = s.Changesets.AddPatchset(ctx, cs.Id, req.ExpectedCurrentPatchsetId, patchset)
	if err != nil {
		return nil, grpcError(err)
	}
	createdPatchset := priorPatchsetID == "" || patchset.Id != priorPatchsetID
	if createdPatchset {
		s.recordBundledCheckRuns(ctx, cs.Id, patchset.Id, req.BundledCheckRuns)
		if s.dispatcher != nil {
			s.dispatcher.cancelOpenCheckRunsBeforePatchset(ctx, cs.Id, patchset.Id)
		}
		s.dispatchCIChecks(ctx, cs, slice, patchset, req.BundledCheckRuns)
	}
	if patchset.Author != "" {
		usernames, err := s.Auth.UsernamesForSubjects(ctx, []string{patchset.Author})
		if err != nil {
			return nil, grpcError(err)
		}
		if username := usernames[patchset.Author]; username != "" {
			patchset.Author = username
		}
	}
	if createdPatchset {
		captureAnalytics(ctx, s.Analytics, analytics.EventPatchsetPushed, map[string]any{
			analytics.PropChangesetID: cs.Id,
			analytics.PropPatchsetID:  patchset.Id,
		})
	}
	return patchset, nil
}

// requireEditBlobsAccessible rejects edits that reference content hashes the
// authoring slice cannot already access, closing the claim-by-hash hole at
// ingestion. BlobId references are resolved to their content hash first.
func requireEditBlobsAccessible(ctx context.Context, blobs storage.BlobStore, slice *corev1.Slice, edits []*corev1.FileEdit) error {
	type blobReference struct {
		contentHash string
		path        string
	}

	references := make([]blobReference, 0, len(edits))
	hashes := make([]string, 0, len(edits))
	seenHashes := make(map[string]struct{}, len(edits))
	resolvedBlobIDs := make(map[string]string)
	unavailable := func(path string) error {
		return status.Errorf(codes.FailedPrecondition, "content for %s is not available to this slice; upload the blob first", path)
	}
	// Resolve the blob ids in one query; any it misses are looked up (and
	// reported) one by one below.
	var ids []string
	for _, edit := range edits {
		if edit != nil && edit.BlobId != "" {
			ids = append(ids, edit.BlobId)
		}
	}
	if len(ids) > 0 {
		found, err := blobs.GetByIDs(ctx, ids)
		if err != nil {
			return grpcError(err)
		}
		for id, blob := range found {
			resolvedBlobIDs[id] = blob.ContentHash
		}
	}

	for _, edit := range edits {
		if edit == nil {
			continue
		}
		normalized, err := normalizeEdit(edit, false)
		if err != nil {
			// The normal validator reports malformed edits as InvalidArgument.
			// They cannot be ingested, so leave their error precedence unchanged.
			continue
		}
		edit = normalized
		path := edit.Path
		if path == "" {
			path = edit.OldPath
		}

		contentHash := edit.ContentHash
		if edit.BlobId != "" {
			var ok bool
			contentHash, ok = resolvedBlobIDs[edit.BlobId]
			if !ok {
				blob, err := blobs.GetByID(ctx, edit.BlobId)
				if errors.Is(err, storage.ErrNotFound) {
					return unavailable(path)
				}
				if err != nil {
					return grpcError(err)
				}
				contentHash = blob.ContentHash
				resolvedBlobIDs[edit.BlobId] = contentHash
			}
		}
		if contentHash == "" {
			continue
		}
		if _, ok := seenHashes[contentHash]; ok {
			continue
		}
		seenHashes[contentHash] = struct{}{}
		hashes = append(hashes, contentHash)
		references = append(references, blobReference{contentHash: contentHash, path: path})
	}
	if len(hashes) == 0 {
		return nil
	}

	accessible, err := accessibleBlobHashes(ctx, blobs, slice, hashes)
	if err != nil {
		return grpcError(err)
	}
	for _, reference := range references {
		if !accessible[reference.contentHash] {
			return unavailable(reference.path)
		}
	}
	return nil
}

func (s *ChangesetService) recordBundledCheckRuns(ctx context.Context, changesetID, patchsetID string, bundled []*corev1.BundledCheckRun) {
	if len(bundled) == 0 || s.Checks == nil {
		return
	}
	for _, bundledRun := range bundled {
		if bundledRun == nil {
			continue
		}
		run, err := s.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
			ChangesetID: changesetID,
			PatchsetID:  patchsetID,
			CheckName:   bundledRun.Name,
			Provenance:  "self",
			Status:      "running",
		})
		if err != nil {
			slog.Warn("failed to create bundled check run", "changeset_id", changesetID, "patchset_id", patchsetID, "check", bundledRun.Name, "error", err)
			continue
		}
		status := strings.ToLower(strings.TrimSpace(bundledRun.Status))
		summary := bundledRun.Summary
		if status != "passed" && status != "failed" && status != "errored" && status != "skipped" {
			status = "errored"
			summary = fmt.Sprintf("invalid bundled check status %q", strings.TrimSpace(bundledRun.Status))
		}
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, run.Id, status, bundledRun.ExitCode, summary); err != nil {
			slog.Warn("failed to record bundled check status", "run_id", run.Id, "check", bundledRun.Name, "status", status, "error", err)
		}
		if bundledRun.Log != "" {
			if _, err := s.Checks.AppendCheckRunLog(ctx, run.Id, 1, "stdout", bundledRun.Log); err != nil {
				slog.Warn("failed to record bundled check log", "run_id", run.Id, "check", bundledRun.Name, "error", err)
			}
		}
	}
}

// applyAuthoringConversation stamps the agent conversation that produced a
// patchset, recording the conversation's current latest event seq as the cutoff
// so the patchset's exchange is the events in (prevCutoff, seq]. The conversation
// must belong to the same slice as the changeset.
func (s *ChangesetService) applyAuthoringConversation(ctx context.Context, patchset *corev1.Patchset, sliceID, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" || s.Agents == nil {
		return nil
	}
	conv, err := s.Agents.GetConversation(ctx, conversationID)
	if err != nil {
		return grpcError(err)
	}
	if conv.SliceId != sliceID {
		return status.Error(codes.InvalidArgument, "conversation does not belong to the changeset slice")
	}
	seq, err := s.Agents.LatestEventSeq(ctx, conversationID)
	if err != nil {
		return grpcError(err)
	}
	patchset.AuthoringConversationId = conversationID
	patchset.AuthoringConversationSeq = seq
	return nil
}

func (s *ChangesetService) patchsetBaseSource(ctx context.Context, cs *corev1.Changeset, req *corev1.UpdateChangesetRequest, baseCommitID string) (baseKind, basePatchsetID, baseTreeID string, err error) {
	baseKind = strings.TrimSpace(req.BaseKind)
	if baseKind == "" {
		if req.BasePatchsetId != "" || cs.ParentChangesetId != "" {
			baseKind = "patchset"
		} else {
			baseKind = "commit"
		}
	}
	switch baseKind {
	case "commit":
		baseTreeID, err = s.Repository.RootTreeForCommit(ctx, baseCommitID)
		if err != nil {
			return "", "", "", grpcError(err)
		}
		return baseKind, "", baseTreeID, nil
	case "patchset":
		parentID := strings.TrimSpace(cs.ParentChangesetId)
		if parentID == "" {
			return "", "", "", status.Error(codes.InvalidArgument, "patchset base requires a parent changeset")
		}
		parent, err := s.Changesets.Get(ctx, parentID)
		if err != nil {
			return "", "", "", grpcError(err)
		}
		if cs.StackId == "" || parent.StackId != cs.StackId || parent.TargetRef != cs.TargetRef || !sameSliceRef(parent.AuthoringSlice, cs.AuthoringSlice) {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent changeset is not in the same stack")
		}
		currentParentPatchset := currentPatchset(parent)
		if currentParentPatchset == nil {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent changeset has no current patchset")
		}
		basePatchsetID = strings.TrimSpace(req.BasePatchsetId)
		if basePatchsetID == "" {
			basePatchsetID = currentParentPatchset.Id
		}
		if req.ExpectedParentPatchsetId != "" && req.ExpectedParentPatchsetId != basePatchsetID {
			return "", "", "", status.Error(codes.Aborted, "parent patchset changed")
		}
		if basePatchsetID != currentParentPatchset.Id {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent patchset is not current")
		}
		if currentParentPatchset.ResultTreeId == "" {
			return "", "", "", status.Error(codes.FailedPrecondition, "parent patchset has no result tree")
		}
		return baseKind, basePatchsetID, currentParentPatchset.ResultTreeId, nil
	default:
		return "", "", "", status.Errorf(codes.InvalidArgument, "unsupported base kind %q", baseKind)
	}
}
