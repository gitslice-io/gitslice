package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if req.AuthoringSlice == nil {
		return nil, storage.ErrInvalid
	}
	sliceID := s.b.sliceRefs[sliceRefKey(req.AuthoringSlice)]
	if sliceID == "" {
		return nil, storage.ErrNotFound
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		ref := s.b.refs[targetRef]
		if ref == nil {
			return nil, storage.ErrNotFound
		}
		baseCommitID = ref.CommitId
	}
	title, description := storage.NormalizeChangesetText(req.Title, req.Description)
	id := s.b.nextIDLocked("cs")
	number := s.b.nextChangesetNumberLocked(sliceID)
	ref := cloneSliceRef(req.AuthoringSlice)
	cs := &corev1.Changeset{
		Id:             id,
		AuthoringSlice: ref,
		Author:         subjectID,
		TargetRef:      targetRef,
		BaseCommitId:   baseCommitID,
		Status:         "draft",
		Title:          title,
		Description:    description,
		Number:         number,
	}
	if req.StackId != "" {
		if err := s.b.attachChangesetToStackLocked(cs, req.StackId, req.ParentChangesetId, req.ParentPatchsetId); err != nil {
			return nil, err
		}
	}
	storage.PopulateChangesetHandles(cs)
	s.b.changesets[id] = cloneChangeset(cs)
	return cloneChangeset(cs), nil
}

func (s *ChangesetStore) Get(ctx context.Context, changesetID string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) List(ctx context.Context, req *corev1.ListChangesetsRequest) ([]*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.Changeset
	for _, cs := range s.b.changesets {
		if req.AuthoringSlice != nil && !sameSliceRef(cs.AuthoringSlice, req.AuthoringSlice) {
			continue
		}
		if req.Status != "" && cs.Status != req.Status {
			continue
		}
		clone := cloneChangeset(cs)
		storage.PopulateChangesetHandles(clone)
		if req.Summary {
			// Like the Postgres store: the current patchset's submit
			// requirements stay, the patchsets go.
			for _, patchset := range clone.Patchsets {
				if patchset.GetId() == clone.CurrentPatchsetId && patchset.SubmitRequirements != nil {
					clone.SubmitRequirements = patchset.SubmitRequirements
				}
			}
			if clone.SubmitRequirements == nil {
				clone.SubmitRequirements = &corev1.SubmitRequirements{}
			}
			clone.Patchsets = nil
		}
		out = append(out, clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, nil
}

func (s *ChangesetStore) PatchsetsByConversation(ctx context.Context, conversationID string) ([]*corev1.Patchset, error) {
	if conversationID == "" {
		return nil, nil
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.Patchset
	for _, cs := range s.b.changesets {
		for _, patchset := range cs.Patchsets {
			if patchset.GetAuthoringConversationId() != conversationID {
				continue
			}
			out = append(out, clonePatchset(patchset))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetAuthoringConversationSeq() == out[j].GetAuthoringConversationSeq() {
			return out[i].GetId() < out[j].GetId()
		}
		return out[i].GetAuthoringConversationSeq() < out[j].GetAuthoringConversationSeq()
	})
	return out, nil
}

func (s *ChangesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetId != expectedCurrentPatchsetID {
		return nil, storage.ErrConflict
	}
	next := clonePatchset(patchset)
	if next.BaseKind == "" {
		next.BaseKind = "commit"
	}
	if next.BaseKind == "patchset" && next.BasePatchsetId == "" {
		next.BasePatchsetId = next.StackParentPatchsetId
	}
	if next.StackParentPatchsetId == "" && next.BaseKind == "patchset" {
		next.StackParentPatchsetId = next.BasePatchsetId
	}
	baseTreeID, err := s.b.baseTreeForPatchsetLocked(next)
	if err != nil {
		return nil, err
	}
	next.BaseTreeId = baseTreeID
	resultTreeID, err := s.b.previewTreeForPatchsetLocked(baseTreeID, next.FileEdits)
	if err != nil {
		return nil, err
	}
	next.ResultTreeId = resultTreeID
	if current := currentPatchset(cs); current != nil && len(next.Conflicts) == 0 &&
		current.ResultTreeId == next.ResultTreeId && current.BaseTreeId == next.BaseTreeId {
		return clonePatchset(current), nil
	}
	next.Id = s.b.nextIDLocked("ps")
	next.ChangesetId = changesetID
	next.Number = int64(len(cs.Patchsets) + 1)
	if next.SubmitRequirements == nil || next.SubmitRequirements.SourceSliceDefinitionHash == "" {
		sliceID := s.b.sliceRefs[sliceRefKey(cs.AuthoringSlice)]
		slice := s.b.slices[sliceID]
		next.SubmitRequirements = submitRequirementsForMemorySlice(slice)
	}
	cs.Patchsets = append(cs.Patchsets, next)
	cs.CurrentPatchsetId = next.Id
	cs.CurrentPatchsetNumber = next.Number
	cs.SubmitBlockedReason = ""
	if cs.StackId != "" {
		if stack := s.b.stacks[cs.StackId]; stack != nil {
			if entry := stackEntryByChangeset(stack, cs.Id); entry != nil {
				if next.BaseKind == "patchset" {
					entry.ParentPatchsetId = next.BasePatchsetId
					cs.ParentPatchsetId = next.BasePatchsetId
				}
				entry.State = "draft"
				stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
		s.b.markChildrenStaleLocked(cs.StackId, cs.Id, next.Id)
	}
	return clonePatchset(next), nil
}

func (s *ChangesetStore) Approve(ctx context.Context, changesetID, subjectID string) (*corev1.ApproveChangesetResponse, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, storage.ErrConflict
	}
	if cs.Status == "abandoned" || cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	key := patchsetRequirementKey(changesetID, patchset.Id)
	if s.b.approvals[key] == nil {
		s.b.approvals[key] = map[string]struct{}{}
	}
	s.b.approvals[key][strings.TrimSpace(subjectID)] = struct{}{}
	clearRequirementBlockLocked(cs, patchset.Id)
	return &corev1.ApproveChangesetResponse{ChangesetId: changesetID, PatchsetId: patchset.Id, SubjectId: strings.TrimSpace(subjectID)}, nil
}

func (s *ChangesetStore) ReportCheckResult(ctx context.Context, changesetID, subjectID, checkName, resultStatus string) (*corev1.ReportCheckResultResponse, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, storage.ErrConflict
	}
	if cs.Status == "abandoned" || cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	checkName = strings.TrimSpace(checkName)
	resultStatus, ok := storage.NormalizeCheckStatus(resultStatus)
	if checkName == "" || !ok {
		return nil, storage.ErrInvalid
	}
	key := patchsetRequirementKey(changesetID, patchset.Id)
	if s.b.checkResults[key] == nil {
		s.b.checkResults[key] = map[string]string{}
	}
	s.b.checkResults[key][checkName] = resultStatus
	clearRequirementBlockLocked(cs, patchset.Id)
	return &corev1.ReportCheckResultResponse{ChangesetId: changesetID, PatchsetId: patchset.Id, CheckName: checkName, Status: resultStatus}, nil
}

// clearRequirementBlockLocked is the Postgres store's clearRequirementBlockTx.
func clearRequirementBlockLocked(cs *corev1.Changeset, patchsetID string) {
	if current := currentPatchset(cs); current != nil && current.Id == patchsetID && storage.IsRequirementBlock(cs.SubmitBlockedReason) {
		cs.SubmitBlockedReason = ""
	}
}

func (s *ChangesetStore) UpdateInfo(ctx context.Context, changesetID string, title, description *string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status != "draft" {
		return nil, fmt.Errorf("%w: a changeset's title and description can be changed while it is open; this one is %s", storage.ErrConflict, cs.Status)
	}
	nextTitle, nextDescription := cs.Title, cs.Description
	if title != nil {
		nextTitle = *title
	}
	if description != nil {
		nextDescription = *description
	}
	nextTitle, nextDescription = storage.NormalizeChangesetText(nextTitle, nextDescription)
	if nextTitle == "" {
		return nil, fmt.Errorf("%w: a changeset needs a title", storage.ErrInvalid)
	}
	cs.Title, cs.Description = nextTitle, nextDescription
	return cloneChangeset(cs), nil
}

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
	return s.SubmitWithCheckStatuses(ctx, changesetID, expectedCurrentPatchsetID, nil)
}

func (s *ChangesetStore) SubmitWithCheckStatuses(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (res *corev1.SubmitChangesetResponse, err error) {
	return s.submit(ctx, changesetID, expectedCurrentPatchsetID, extraCheckStatuses, false)
}

// SubmitBypassingRequirements submits without the slice's required approvals
// and checks, for a changeset that is a single copy or move.
func (s *ChangesetStore) SubmitBypassingRequirements(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
	return s.submit(ctx, changesetID, expectedCurrentPatchsetID, nil, true)
}

func (s *ChangesetStore) submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string, bypassRequirements bool) (res *corev1.SubmitChangesetResponse, err error) {
	defer func() {
		storage.RecordSubmitResult(err)
	}()
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetId != expectedCurrentPatchsetID {
		return nil, storage.ErrConflict
	}
	if cs.Status == "pending_publish" {
		return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: cs.Status, PendingPublishId: cs.Id}, nil
	}
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, storage.ErrConflict
	}
	if cs.ParentChangesetId != "" {
		parent := s.b.changesets[cs.ParentChangesetId]
		if parent == nil {
			return nil, storage.ErrNotFound
		}
		if parent.Status != "submitted" && parent.Status != "pending_publish" {
			return nil, s.b.blockSubmitLocked(cs, "BlockedOnBaseChangeset")
		}
	}
	if len(patchset.Conflicts) > 0 {
		return nil, fmt.Errorf("%w: unresolved patchset conflicts", storage.ErrConflict)
	}
	sliceID := s.b.sliceRefs[sliceRefKey(cs.AuthoringSlice)]
	slice := s.b.slices[sliceID]
	latestReq := submitRequirementsForMemorySlice(slice)
	for _, p := range patchset.ChangedPaths {
		if slice == nil || slice.Definition == nil || !pathInAnyPrefix(slice.Definition.IncludedPaths, p) {
			return nil, s.b.blockSubmitLocked(cs, fmt.Sprintf("changed path %s is outside latest slice definition, refresh the changeset", p))
		}
	}
	key := patchsetRequirementKey(changesetID, patchset.Id)
	approvalSubjects := make([]string, 0, len(s.b.approvals[key]))
	for subjectID := range s.b.approvals[key] {
		approvalSubjects = append(approvalSubjects, subjectID)
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
	if reason := storage.EvaluateSubmitRequirements(latestReq, cs.Author, approvalSubjects, checkStatuses); reason != "" && !bypassRequirements {
		return nil, s.b.blockSubmitLocked(cs, reason)
	}
	cs.Status = "pending_publish"
	cs.SubmitBlockedReason = ""
	s.b.pendingAcceptedAt[cs.Id] = time.Now()
	s.b.nextPendingSeq++
	s.b.pendingSequence[cs.Id] = s.b.nextPendingSeq
	return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: cs.Status, PendingPublishId: cs.Id}, nil
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
		ref := s.b.refs[cs.TargetRef]
		if ref == nil {
			return published, storage.ErrNotFound
		}
		baseFiles := cloneFileMap(s.b.commitFiles[ref.CommitId])
		baseDirs := cloneDirSet(s.b.commitDirs[ref.CommitId])
		for _, edit := range patchset.FileEdits {
			switch edit.Op {
			case "delete":
				delete(baseFiles, edit.Path)
				delete(baseDirs, edit.Path)
			case "rename":
				file, ok := baseFiles[edit.OldPath]
				if ok {
					delete(baseFiles, edit.OldPath)
					file.Path = edit.Path
					baseFiles[edit.Path] = file
				}
				if _, ok := baseDirs[edit.OldPath]; ok {
					delete(baseDirs, edit.OldPath)
					baseDirs[edit.Path] = struct{}{}
				}
			case "copy":
				copyMemoryPath(baseFiles, baseDirs, edit.OldPath, edit.Path)
			case "mkdir":
				baseDirs[edit.Path] = struct{}{}
			default:
				blob := s.b.blobs[edit.BlobId]
				if blob == nil && edit.ContentHash == "" {
					return published, storage.ErrNotFound
				}
				contentHash := edit.ContentHash
				size := int64(0)
				if blob != nil {
					contentHash = blob.ContentHash
					size = blob.Size
				}
				baseFiles[edit.Path] = storage.FileEntry{Path: edit.Path, BlobID: edit.BlobId, ContentHash: contentHash, Mode: edit.Mode, Size: size}
			}
		}
		files := make([]storage.FileEntry, 0, len(baseFiles))
		for _, file := range baseFiles {
			files = append(files, file)
		}
		commitID := s.b.nextIDLocked("commit")
		s.b.putCommitWithFilesAndDirsLocked(commitID, files, baseDirs, patchset.ChangedPaths, storage.CommitMessage(cs.Title, cs.Description))
		cs.Status = "submitted"
		cs.CommitId = commitID
		if acceptedAt, ok := s.b.pendingAcceptedAt[cs.Id]; ok {
			storage.ObservePublishLatency(time.Since(acceptedAt))
			delete(s.b.pendingAcceptedAt, cs.Id)
		}
		delete(s.b.pendingSequence, cs.Id)
		if _, err := s.b.appendWebhookEventLocked(storage.WebhookEvent{
			Kind:         storage.WebhookEventChangesetSubmitted,
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

func (s *ChangesetStore) Abandon(ctx context.Context, changesetID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return storage.ErrNotFound
	}
	if cs.Status == "submitted" {
		return storage.ErrConflict
	}
	for _, child := range s.b.changesets {
		if child != nil && child.ParentChangesetId == changesetID && child.Status != "submitted" && child.Status != "abandoned" {
			return storage.ErrConflict
		}
	}
	cs.Status = "abandoned"
	s.b.refreshStackStatusLocked(cs.StackId)
	return nil
}

func patchsetRequirementKey(changesetID, patchsetID string) string {
	return changesetID + "\x00" + patchsetID
}

func submitRequirementsForMemorySlice(slice *corev1.Slice) *corev1.SubmitRequirements {
	req := &corev1.SubmitRequirements{}
	if slice == nil {
		return req
	}
	if slice.Definition != nil {
		req.RequiredApprovals = slice.Definition.RequiredApprovals
		req.RequiredChecks = append([]string(nil), slice.Definition.RequiredChecks...)
		req.SourceSliceDefinitionHash = storage.SubmitRequirementsHash(
			slice.Definition.IncludedPaths,
			slice.Definition.RequiredApprovals,
			slice.Definition.RequiredChecks,
		)
	}
	return req
}

func (b *backend) blockSubmitLocked(cs *corev1.Changeset, reason string) error {
	cs.SubmitBlockedReason = reason
	return fmt.Errorf("%w: %s", storage.ErrConflict, reason)
}

func sortedChangesets(changesets map[string]*corev1.Changeset) []*corev1.Changeset {
	out := make([]*corev1.Changeset, 0, len(changesets))
	for _, cs := range changesets {
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out
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

func currentPatchset(cs *corev1.Changeset) *corev1.Patchset {
	for _, patchset := range cs.Patchsets {
		if patchset.Id == cs.CurrentPatchsetId {
			return patchset
		}
	}
	if len(cs.Patchsets) == 0 {
		return nil
	}
	return cs.Patchsets[len(cs.Patchsets)-1]
}

func changesetHasPatchsetID(cs *corev1.Changeset, patchsetID string) bool {
	if cs == nil {
		return false
	}
	for _, patchset := range cs.Patchsets {
		if patchset != nil && patchset.Id == patchsetID {
			return true
		}
	}
	return false
}
