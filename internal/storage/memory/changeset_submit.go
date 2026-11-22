package memory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
	return s.SubmitChangeset(ctx, storage.SubmitInput{
		ChangesetID:               changesetID,
		ExpectedCurrentPatchsetID: expectedCurrentPatchsetID,
	})
}

func (s *ChangesetStore) SubmitChangeset(ctx context.Context, in storage.SubmitInput) (res *corev1.SubmitChangesetResponse, err error) {
	return s.submit(ctx, in.ChangesetID, in.ExpectedCurrentPatchsetID, in.ExtraCheckStatuses, in.ExtraRequiredChecks, in.BypassRequirements)
}

// RecordSubmitBlockedReason records a service-enforced requirement block (such
// as path owners or .gitslice approval) onto a draft changeset.
func (s *ChangesetStore) RecordSubmitBlockedReason(_ context.Context, changesetID, expectedCurrentPatchsetID, reason string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	resolved, err := s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}
	cs := s.b.changesets[resolved]
	if cs != nil && cs.Status == "draft" && cs.CurrentPatchsetId == strings.TrimSpace(expectedCurrentPatchsetID) {
		cs.SubmitBlockedReason = reason
		if reason == "" {
			delete(s.b.submitBlockedCodes, cs.Id)
		} else {
			s.b.submitBlockedCodes[cs.Id] = storage.ClassifySubmitBlockedReason(reason)
		}
	}
	return nil
}

func (s *ChangesetStore) submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string, extraRequiredChecks []string, bypassRequirements bool) (res *corev1.SubmitChangesetResponse, err error) {
	defer func() {
		storage.RecordSubmitResult(err)
	}()
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status == "abandoned" {
		return nil, storage.ErrConflict
	}
	if cs.Status == "submitted" && cs.CommitId != "" {
		return &corev1.SubmitChangesetResponse{CommitId: cs.CommitId, TargetRef: cs.TargetRef, NewRefCommitId: cs.CommitId, Status: "submitted"}, nil
	}
	if cs.Status == "pending_publish" {
		return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: cs.Status, PendingPublishId: cs.Id}, nil
	}
	patchset := currentPatchset(cs)
	currentPatchsetID := cs.CurrentPatchsetId
	if patchset == nil {
		currentPatchsetID = ""
	}
	if err := storage.ValidateSubmitDraftHeader(cs.Status, currentPatchsetID, expectedCurrentPatchsetID); err != nil {
		return nil, err
	}
	var parentStatus string
	if cs.ParentChangesetId != "" {
		parent := s.b.changesets[cs.ParentChangesetId]
		if parent == nil {
			return nil, storage.ErrNotFound
		}
		parentStatus = parent.Status
	}
	sliceID := s.b.sliceRefs[sliceRefKey(cs.AuthoringSlice)]
	slice := s.b.slices[sliceID]
	if slice == nil {
		return nil, storage.ErrNotFound
	}
	var includedPaths []string
	if slice.Definition != nil {
		includedPaths = slice.Definition.IncludedPaths
	}
	blockCode, blockReason, err := storage.ValidateSubmitPreconditions(storage.SubmitPreconditionInput{
		Status:                    cs.Status,
		CurrentPatchsetID:         currentPatchsetID,
		ExpectedCurrentPatchsetID: expectedCurrentPatchsetID,
		HasConflicts:              len(patchset.Conflicts) > 0,
		HasParent:                 cs.ParentChangesetId != "",
		ParentStatus:              parentStatus,
		SliceArchived:             slice.Archived,
		SliceDeleted:              s.b.retiredSlices[sliceID],
		ChangedPaths:              patchset.ChangedPaths,
		IncludedPaths:             includedPaths,
	})
	if err != nil {
		return nil, err
	}
	if blockReason != "" {
		return nil, s.b.blockSubmitLocked(cs, blockCode, blockReason)
	}

	latestReq := submitRequirementsForMemorySlice(slice)
	key := patchsetRequirementKey(changesetID, patchset.Id)
	approvers := make([]storage.Subject, 0, len(s.b.approvals[key]))
	for subjectID := range s.b.approvals[key] {
		approvers = append(approvers, s.b.subjectOrIDLocked(subjectID))
	}
	excludedAuthors := []storage.Subject{s.b.subjectOrIDLocked(cs.Author)}
	for _, ps := range cs.Patchsets {
		if ps == nil || ps.Kind == storage.SubmitMergeKind {
			continue
		}
		excludedAuthors = append(excludedAuthors, s.b.subjectOrIDLocked(ps.Author))
	}
	checkStatuses := map[string]string{}
	for checkName, status := range s.b.checkResults[key] {
		checkStatuses[checkName] = status
	}
	for checkName, status := range extraCheckStatuses {
		checkName = strings.TrimSpace(checkName)
		normalized, ok := storage.NormalizeCheckStatus(status)
		if checkName == "" || !ok {
			continue
		}
		checkStatuses[checkName] = normalized
	}
	var approverGroupMembers map[string]bool
	if latestReq.GetApprovers() != "" && !bypassRequirements {
		approverGroupMembers = s.b.groupMemberSubjectIDsLocked(latestReq.GetApprovers())
	}
	evalReq := storage.MergeRequiredChecks(latestReq, extraRequiredChecks)
	if code, reason := storage.EvaluateSubmitRequirementsWithCode(evalReq, excludedAuthors, approvers, checkStatuses, approverGroupMembers); reason != "" && !bypassRequirements {
		return nil, s.b.blockSubmitLocked(cs, code, reason)
	}

	if err := s.b.validateAcceptedPathBasesLocked(cs.TargetRef, patchset.PathBases); err != nil {
		if !errors.Is(err, storage.ErrConflict) {
			return nil, err
		}
		return nil, s.b.blockSubmitLocked(cs, storage.SubmitBlockCodeStalePathBase, storage.StaleBaseReason)
	}

	submitAttr := s.b.resolveAttributionLocked(ctx, "")
	cs.Status = "pending_publish"
	cs.SubmitBlockedReason = ""
	delete(s.b.submitBlockedCodes, cs.Id)
	if submitAttr.ActingSubjectID != "" {
		cs.SubmittedAttribution = submitAttr.ToProto()
	}
	s.b.pendingAcceptedAt[cs.Id] = time.Now()
	s.b.nextPendingSeq++
	s.b.pendingSequence[cs.Id] = s.b.nextPendingSeq
	return &corev1.SubmitChangesetResponse{
		TargetRef:        cs.TargetRef,
		Status:           cs.Status,
		PendingPublishId: cs.Id,
		Attribution:      cloneChangeset(&corev1.Changeset{Attribution: cs.SubmittedAttribution}).GetAttribution(),
	}, nil
}

func (b *backend) validateAcceptedPathBasesLocked(targetRef string, bases []*corev1.PathBase) error {
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	files, dirs, err := b.acceptedRefSnapshotLocked(targetRef)
	if err != nil {
		return err
	}
	for _, base := range bases {
		head := pathHeadFromMemorySnapshot(files, dirs, base.GetPath())
		if err := storage.ValidateAcceptedPathBase(head, base); err != nil {
			return err
		}
	}
	return nil
}

func (b *backend) acceptedRefSnapshotLocked(targetRef string) (map[string]storage.FileEntry, map[string]struct{}, error) {
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	ref := b.refs[targetRef]
	if ref == nil {
		return nil, nil, storage.ErrNotFound
	}
	files := cloneFileMap(b.commitFiles[ref.CommitId])
	dirs := cloneDirSet(b.commitDirs[ref.CommitId])
	for _, pending := range b.pendingChangesetsInSequenceLocked() {
		pendingRef := pending.TargetRef
		if pendingRef == "" {
			pendingRef = storage.DefaultTargetRef
		}
		if pendingRef != targetRef {
			continue
		}
		ps := currentPatchset(pending)
		if ps == nil {
			continue
		}
		if err := b.applyMemoryFileEditsLocked(files, dirs, ps.FileEdits); err != nil {
			return nil, nil, err
		}
	}
	return files, dirs, nil
}

func pathHeadFromMemorySnapshot(files map[string]storage.FileEntry, dirs map[string]struct{}, p string) storage.PathHead {
	entry, err := treeEntryFromMemorySnapshot(files, dirs, p)
	if err != nil {
		return storage.PathHead{
			Path:             p,
			Exists:           false,
			EntryFingerprint: storage.MissingEntryFingerprint(),
		}
	}
	return storage.PathHeadFromTreeEntry(*entry)
}

func (b *backend) applyMemoryFileEditsLocked(files map[string]storage.FileEntry, dirs map[string]struct{}, edits []*corev1.FileEdit) error {
	for _, edit := range edits {
		switch edit.Op {
		case "delete":
			deleteMemoryPath(files, dirs, edit.Path)
		case "rename":
			renameMemoryPath(files, dirs, edit.OldPath, edit.Path)
		case "copy":
			copyMemoryPath(files, dirs, edit.OldPath, edit.Path)
		case "mkdir":
			ensureMemoryDir(dirs, edit.Path)
		default:
			blob := b.blobs[edit.BlobId]
			if blob == nil && edit.ContentHash == "" {
				return storage.ErrNotFound
			}
			contentHash := edit.ContentHash
			size := int64(0)
			if blob != nil {
				contentHash = blob.ContentHash
				size = blob.Size
			}
			ensureMemoryParentDirs(dirs, edit.Path)
			files[edit.Path] = storage.FileEntry{Path: edit.Path, BlobID: edit.BlobId, ContentHash: contentHash, Mode: edit.Mode, Size: size}
		}
	}
	return nil
}

func (s *ChangesetStore) PublishPending(ctx context.Context, limit int) (published int, err error) {
	defer func() {
		storage.RecordPublishBatch(published, err)
	}()
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 128
	}
	for _, cs := range s.b.pendingChangesetsInSequenceLocked() {
		if published >= limit {
			break
		}
		if cs.Status != "pending_publish" {
			continue
		}
		patchset := currentPatchset(cs)
		if patchset == nil {
			continue
		}
		targetRef := cs.TargetRef
		if targetRef == "" {
			targetRef = storage.DefaultTargetRef
		}
		ref := s.b.refs[targetRef]
		if ref == nil {
			return published, storage.ErrNotFound
		}
		baseFiles := cloneFileMap(s.b.commitFiles[ref.CommitId])
		baseDirs := cloneDirSet(s.b.commitDirs[ref.CommitId])
		if err := s.b.applyMemoryFileEditsLocked(baseFiles, baseDirs, patchset.FileEdits); err != nil {
			return published, err
		}
		files := make([]storage.FileEntry, 0, len(baseFiles))
		for _, file := range baseFiles {
			files = append(files, file)
		}
		commitID := s.b.nextIDLocked("commit")
		s.b.putCommitWithFilesAndDirsLocked(commitID, files, baseDirs, patchset.ChangedPaths, storage.CommitMessage(cs.Title, cs.Description))
		cs.Status = "submitted"
		cs.CommitId = commitID
		s.b.clearSubmittedPathIntentsLocked(cs, patchset.ChangedPaths)
		if acceptedAt, ok := s.b.pendingAcceptedAt[cs.Id]; ok {
			storage.ObservePublishLatency(time.Since(acceptedAt))
			delete(s.b.pendingAcceptedAt, cs.Id)
		}
		delete(s.b.pendingSequence, cs.Id)
		if _, err := s.b.appendEventLocked(storage.Event{
			Kind:         storage.EventChangesetSubmitted,
			ChangesetID:  cs.Id,
			PatchsetID:   patchset.Id,
			CommitID:     commitID,
			TargetRef:    cs.TargetRef,
			ChangedPaths: patchset.ChangedPaths,
		}); err != nil {
			return published, err
		}
		published++
		s.b.refreshStackStatusLocked(cs.StackId)
	}
	return published, nil
}

func (s *ChangesetStore) PublishGitImportBatch(ctx context.Context, in storage.GitImportBatchInput) ([]storage.GitImportPublishedCommit, error) {
	if len(in.Commits) == 0 {
		return nil, nil
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	targetRef := in.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	ref := s.b.refs[targetRef]
	if ref == nil {
		return nil, storage.ErrNotFound
	}
	if in.ImportID != "" && s.b.imports[in.ImportID] == nil {
		return nil, storage.ErrNotFound
	}
	// Validate all edits against a staged copy before mutating backend state so
	// a missing blob leaves the batch atomic.
	stageFiles := cloneFileMap(s.b.commitFiles[ref.CommitId])
	stageDirs := cloneDirSet(s.b.commitDirs[ref.CommitId])
	type stagedCommit struct {
		spec  storage.GitImportCommitSpec
		files []storage.FileEntry
		dirs  map[string]struct{}
	}
	staged := make([]stagedCommit, 0, len(in.Commits))
	for _, spec := range in.Commits {
		if err := s.b.applyMemoryFileEditsLocked(stageFiles, stageDirs, spec.FileEdits); err != nil {
			return nil, err
		}
		files := make([]storage.FileEntry, 0, len(stageFiles))
		for _, f := range stageFiles {
			files = append(files, f)
		}
		staged = append(staged, stagedCommit{
			spec:  spec,
			files: files,
			dirs:  cloneDirSet(stageDirs),
		})
	}
	out := make([]storage.GitImportPublishedCommit, 0, len(staged))
	for _, item := range staged {
		commitID := s.b.nextIDLocked("commit")
		msg := storage.CommitMessage(item.spec.Title, item.spec.Description)
		if msg == "" {
			msg = "Import git commit " + item.spec.GitCommitID
		}
		s.b.putCommitWithFilesAndDirsLocked(commitID, item.files, item.dirs, item.spec.ChangedPaths, msg)
		if in.ImportID != "" {
			record := storage.GitImportedCommitRecord{
				ImportID:         in.ImportID,
				GitCommitID:      item.spec.GitCommitID,
				NativeCommitID:   commitID,
				Message:          item.spec.Title,
				Position:         item.spec.Position,
				ChangedPathCount: len(item.spec.FileEdits),
				AuthorName:       item.spec.AuthorName,
				AuthorEmail:      item.spec.AuthorEmail,
				AuthoredAt:       item.spec.AuthoredAt,
				FullMessage:      item.spec.FullMessage,
			}
			replaced := false
			for i, existing := range s.b.importedCommits[in.ImportID] {
				if existing.GitCommitID == record.GitCommitID {
					s.b.importedCommits[in.ImportID][i] = record
					replaced = true
					break
				}
			}
			if !replaced {
				s.b.importedCommits[in.ImportID] = append(s.b.importedCommits[in.ImportID], record)
				s.b.imports[in.ImportID].ImportedCount++
			}
			s.b.imports[in.ImportID].LastGitCommitID = record.GitCommitID
			s.b.imports[in.ImportID].FinalNativeCommitID = record.NativeCommitID
		}
		out = append(out, storage.GitImportPublishedCommit{
			GitCommitID:    item.spec.GitCommitID,
			NativeCommitID: commitID,
			Message:        item.spec.Title,
		})
	}
	return out, nil
}

func (s *ChangesetStore) PendingPublishDepth(ctx context.Context) (int, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	depth := 0
	for _, cs := range s.b.changesets {
		if cs != nil && cs.Status == "pending_publish" {
			depth++
		}
	}
	return depth, nil
}

func submitRequirementsForMemorySlice(slice *corev1.Slice) *corev1.SubmitRequirements {
	req := &corev1.SubmitRequirements{}
	if slice == nil {
		return req
	}
	if slice.Definition != nil {
		req.RequiredApprovals = slice.Definition.RequiredApprovals
		req.RequiredHumanApprovals = slice.Definition.RequiredHumanApprovals
		req.RequiredChecks = append([]string(nil), slice.Definition.RequiredChecks...)
		req.Approvers = slice.Definition.Approvers
		req.SourceSliceDefinitionHash = storage.SubmitRequirementsHashWithApprovers(
			slice.Definition.IncludedPaths,
			slice.Definition.RequiredApprovals,
			slice.Definition.RequiredHumanApprovals,
			slice.Definition.RequiredChecks,
			slice.Definition.Approvers,
		)
	}
	return req
}

func (b *backend) subjectOrIDLocked(subjectID string) storage.Subject {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return storage.Subject{}
	}
	if subj, ok := b.subjects[subjectID]; ok {
		return subj
	}
	return storage.Subject{ID: subjectID}
}

func (b *backend) blockSubmitLocked(cs *corev1.Changeset, code storage.SubmitBlockCode, reason string) error {
	if code == storage.SubmitBlockCodeNone {
		code = storage.ClassifySubmitBlockedReason(reason)
	}
	cs.SubmitBlockedReason = reason
	b.submitBlockedCodes[cs.Id] = code
	return storage.NewSubmitBlockedError(code, reason)
}

func (b *backend) pendingChangesetsInSequenceLocked() []*corev1.Changeset {
	out := make([]*corev1.Changeset, 0, len(b.changesets))
	for _, cs := range b.changesets {
		if cs != nil && cs.Status == "pending_publish" {
			out = append(out, cs)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		leftSeq := b.pendingSequence[out[i].Id]
		rightSeq := b.pendingSequence[out[j].Id]
		if leftSeq == rightSeq {
			leftAt := b.pendingAcceptedAt[out[i].Id]
			rightAt := b.pendingAcceptedAt[out[j].Id]
			if leftAt.Equal(rightAt) {
				return out[i].Id < out[j].Id
			}
			return leftAt.Before(rightAt)
		}
		return leftSeq < rightSeq
	})
	return out
}
