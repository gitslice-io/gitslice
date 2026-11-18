package service

import (
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *ChangesetStackService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := s.getStackWithSlice(ctx, req.StackId, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	entries := selectedRestackEntries(stack, req.StartChangesetId, req.IncludeSiblings)
	if len(entries) == 0 {
		if err := s.authorizeStackOwner(ctx, subjectID, stack, authz.ActionPropose); err != nil {
			return nil, err
		}
		return nil, status.Error(codes.NotFound, "stack entry not found")
	}
	if err := s.authorizeStackEntries(ctx, subjectID, entries, authz.ActionPropose, true); err != nil {
		return nil, err
	}
	changesetService := s.changesetService()
	response := &corev1.RestackResponse{StackId: stack.Id, Status: "clean"}
	selectedRoot := false
	for _, entry := range entries {
		if entry.ParentChangesetId == "" {
			selectedRoot = true
		}
		cs, err := s.Changesets.Get(ctx, entry.ChangesetId)
		if err != nil {
			return nil, grpcError(err)
		}
		if cs.Status == "submitted" {
			continue
		}
		current := currentPatchset(cs)
		if current == nil {
			continue
		}
		if entry.ParentChangesetId == "" {
			if strings.TrimSpace(req.TargetBaseCommitId) == "" {
				continue
			}
			update, err := s.rebasedUpdate(ctx, cs, current, req.TargetBaseCommitId, "", &corev1.UpdateChangesetRequest{
				ChangesetId:               cs.Id,
				ExpectedCurrentPatchsetId: cs.CurrentPatchsetId,
				BaseCommitId:              req.TargetBaseCommitId,
				BaseKind:                  "commit",
			})
			if err != nil {
				return nil, err
			}
			refreshed, conflicted, err := s.restackUpdateOrConflict(ctx, changesetService, cs, update)
			if err != nil {
				return nil, err
			}
			if conflicted || len(update.Conflicts) > 0 {
				response.Status = "conflicts"
			}
			response.Entries = append(response.Entries, refreshed)
			continue
		}
		parent, err := s.Changesets.Get(ctx, entry.ParentChangesetId)
		if err != nil {
			return nil, grpcError(err)
		}
		parentPatchset := currentPatchset(parent)
		if parentPatchset == nil || parentPatchset.ResultTreeId == "" {
			return nil, status.Error(codes.FailedPrecondition, "parent changeset has no preview patchset")
		}
		if current.BasePatchsetId == parentPatchset.Id && entry.State != "needs_restack" {
			continue
		}
		update, err := s.rebasedUpdate(ctx, cs, current, cs.BaseCommitId, parentPatchset.ResultTreeId, &corev1.UpdateChangesetRequest{
			ChangesetId:               cs.Id,
			ExpectedCurrentPatchsetId: cs.CurrentPatchsetId,
			BaseCommitId:              cs.BaseCommitId,
			BaseKind:                  "patchset",
			BasePatchsetId:            parentPatchset.Id,
			ExpectedParentPatchsetId:  parentPatchset.Id,
		})
		if err != nil {
			return nil, err
		}
		refreshed, conflicted, err := s.restackUpdateOrConflict(ctx, changesetService, cs, update)
		if err != nil {
			return nil, err
		}
		if conflicted || len(update.Conflicts) > 0 {
			response.Status = "conflicts"
		}
		response.Entries = append(response.Entries, refreshed)
	}
	if err := s.resolveChangesetAuthors(ctx, response.Entries...); err != nil {
		return nil, grpcError(err)
	}
	// The tree now starts at the target commit: its roots moved there, or were
	// submitted and are part of its history. Workspaces compare their base
	// against it before the next sync, status or modify.
	if target := strings.TrimSpace(req.TargetBaseCommitId); selectedRoot && target != "" && target != stack.BaseCommitId {
		if err := s.Changesets.SetStackBaseCommit(ctx, stack.Id, target); err != nil {
			return nil, grpcError(err)
		}
	}
	if stack.Status == "partial" {
		if err := s.Changesets.SetStackStatus(ctx, stack.Id, "open"); err != nil {
			return nil, grpcError(err)
		}
	}
	return response, nil
}

// rebasedUpdate fills an update that moves a changeset's patchset onto a new
// base with its edits taken there (rebaseEdits). Conflicts found, and those
// the patchset still has unresolved, make it a conflict patchset, which
// cannot be submitted until a new patchset resolves them.
func (s *ChangesetStackService) rebasedUpdate(ctx context.Context, cs *corev1.Changeset, current *corev1.Patchset, newBaseCommitID, newBaseTreeID string, update *corev1.UpdateChangesetRequest) (*corev1.UpdateChangesetRequest, error) {
	edits, conflicts, err := s.rebaseEdits(ctx, cs, current, newBaseCommitID, newBaseTreeID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, conflict := range conflicts {
		seen[conflict.GetPath()] = true
	}
	for _, unresolved := range current.GetConflicts() {
		if unresolved != nil && !seen[unresolved.GetPath()] {
			conflicts = append(conflicts, unresolved)
		}
	}
	update.FileEdits = edits
	update.PatchsetKind = current.Kind
	if len(conflicts) > 0 {
		update.Conflicts = conflicts
		update.PatchsetKind = "conflict"
	}
	return update, nil
}

func (s *ChangesetStackService) restackUpdateOrConflict(ctx context.Context, changesetService ChangesetService, cs *corev1.Changeset, req *corev1.UpdateChangesetRequest) (*corev1.Changeset, bool, error) {
	attemptedEdits := cloneFileEdits(req.FileEdits)
	if _, err := changesetService.UpdateChangeset(ctx, req); err != nil {
		if !restackReplayMayBecomeConflict(err) {
			return nil, false, err
		}
		conflictReq := proto.Clone(req).(*corev1.UpdateChangesetRequest)
		conflictReq.FileEdits = nil
		conflictReq.Conflicts = restackConflictsFromEdits(cs, attemptedEdits, req.BaseCommitId)
		conflictReq.PatchsetKind = "conflict"
		if len(conflictReq.Conflicts) == 0 {
			conflictReq.Conflicts = []*corev1.PatchsetConflict{{
				Path:            "/",
				ConflictClass:   "restack",
				OldBaseCommitId: cs.BaseCommitId,
				NewBaseCommitId: req.BaseCommitId,
			}}
		}
		if _, conflictErr := changesetService.UpdateChangeset(ctx, conflictReq); conflictErr != nil {
			return nil, false, err
		}
		refreshed, getErr := s.Changesets.Get(ctx, cs.Id)
		if getErr != nil {
			return nil, false, grpcError(getErr)
		}
		return refreshed, true, nil
	}
	refreshed, err := s.Changesets.Get(ctx, cs.Id)
	if err != nil {
		return nil, false, grpcError(err)
	}
	return refreshed, false, nil
}

func restackReplayMayBecomeConflict(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.FailedPrecondition, codes.NotFound:
		return true
	default:
		return false
	}
}

func restackConflictsFromEdits(cs *corev1.Changeset, edits []*corev1.FileEdit, newBaseCommitID string) []*corev1.PatchsetConflict {
	seen := map[string]*corev1.PatchsetConflict{}
	out := []*corev1.PatchsetConflict{}
	add := func(p string, edit *corev1.FileEdit) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if existing, ok := seen[p]; ok {
			if existing.LocalContentHash == "" && edit != nil && edit.Path == p && edit.ContentHash != "" && edit.Op != "delete" {
				existing.LocalContentHash = edit.ContentHash
			}
			return
		}
		conflict := &corev1.PatchsetConflict{
			Path:            p,
			ConflictClass:   "restack",
			OldBaseCommitId: cs.BaseCommitId,
			NewBaseCommitId: newBaseCommitID,
		}
		if edit != nil && edit.Path == p && edit.ContentHash != "" && edit.Op != "delete" {
			conflict.LocalContentHash = edit.ContentHash
		}
		seen[p] = conflict
		out = append(out, conflict)
	}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		add(edit.Path, edit)
		add(edit.OldPath, edit)
	}
	return out
}

func (s *ChangesetStackService) SubmitStack(ctx context.Context, req *corev1.SubmitStackRequest) (*corev1.SubmitStackResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := s.getStackWithSlice(ctx, req.StackId, authz.ActionSubmit)
	if err != nil {
		return nil, err
	}
	entries := selectedStackEntries(stack, req.SubtreeRootChangesetId)
	if len(entries) == 0 {
		if err := s.authorizeStackOwner(ctx, subjectID, stack, authz.ActionSubmit); err != nil {
			return nil, err
		}
		return nil, status.Error(codes.NotFound, "stack entry not found")
	}
	if err := s.authorizeStackEntries(ctx, subjectID, entries, authz.ActionSubmit, true); err != nil {
		return nil, err
	}
	ctx, _ = withCapturedAttribution(ctx, s.Auth, subjectID)
	response := &corev1.SubmitStackResponse{StackId: stack.Id, Status: "submitted"}
	blocked := false
	for _, entry := range entries {
		cs := entry.Changeset
		if cs == nil {
			var err error
			cs, err = s.Changesets.Get(ctx, entry.ChangesetId)
			if err != nil {
				return nil, grpcError(err)
			}
		}
		if entry.State == "needs_restack" {
			blocked = true
			response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
				ChangesetId:   entry.ChangesetId,
				Status:        "blocked",
				BlockedReason: "NeedsBaseUpdate",
			})
			continue
		}
		if cs.Status == "submitted" {
			response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
				ChangesetId: entry.ChangesetId,
				Status:      "submitted",
				CommitId:    cs.CommitId,
			})
			continue
		}
		var res *corev1.SubmitChangesetResponse
		changesets := s.changesetService()
		extra, err := changesets.submitExtras(ctx, cs)
		if err == nil {
			res, err = changesets.submitPatchset(ctx, entry.ChangesetId, cs.CurrentPatchsetId, extra)
		}
		merged := false
		var mergedMessage string
		if isStaleBase(err) && entry.ParentChangesetId == "" {
			// A root of the tree goes onto the latest base like a changeset
			// submitted alone (merge on submit); its dependents follow it by
			// restack, as they do when it moves any other way.
			changesets := s.changesetService()
			if mergedRes, mergedErr, applied := changesets.mergeAndSubmit(ctx, cs); applied {
				merged = true
				res, err = mergedRes, mergedErr
				if mergedErr != nil {
					mergedMessage = status.Convert(mergedErr).Message()
				}
			}
		}
		if err != nil {
			blocked = true
			reason := submitBlockReason(err)
			if refreshed, getErr := s.Changesets.Get(ctx, entry.ChangesetId); !merged && getErr == nil && refreshed.SubmitBlockedReason != "" {
				reason = refreshed.SubmitBlockedReason
			}
			if merged {
				reason = mergedMessage
			}
			response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
				ChangesetId:   entry.ChangesetId,
				Status:        "blocked",
				BlockedReason: reason,
			})
			continue
		}
		if cs.Status != "pending_publish" && cs.Status != "submitted" {
			changesets.captureChangesetSubmitted(ctx, cs)
		}
		response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
			ChangesetId:      entry.ChangesetId,
			Status:           res.Status,
			CommitId:         res.CommitId,
			PendingPublishId: res.PendingPublishId,
		})
	}
	if blocked {
		response.Status = "partial"
		if err := s.Changesets.SetStackStatus(ctx, stack.Id, "partial"); err != nil {
			return nil, grpcError(err)
		}
		return response, nil
	}
	if stack.Status == "partial" {
		if err := s.Changesets.SetStackStatus(ctx, stack.Id, "open"); err != nil {
			return nil, grpcError(err)
		}
	}
	return response, nil
}

func cloneFileEdits(in []*corev1.FileEdit) []*corev1.FileEdit {
	out := make([]*corev1.FileEdit, 0, len(in))
	for _, edit := range in {
		if edit == nil {
			continue
		}
		out = append(out, proto.Clone(edit).(*corev1.FileEdit))
	}
	return out
}

func submitBlockReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, storage.ErrConflict) {
		return strings.TrimSpace(strings.TrimPrefix(err.Error(), storage.ErrConflict.Error()+":"))
	}
	return err.Error()
}
