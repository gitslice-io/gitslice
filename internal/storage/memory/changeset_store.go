package memory

import (
	"context"
	"fmt"
	"slices"
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
	attr := s.b.resolveAttributionLocked(ctx, subjectID)
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
		Attribution:    attr.ToProto(),
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

// List filters like the Postgres store. It has no update times, so it lists in
// id order and its token is the last id returned.
func (s *ChangesetStore) List(ctx context.Context, filter storage.ChangesetListFilter) ([]*corev1.Changeset, string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	var matched []*corev1.Changeset
	for _, cs := range s.b.changesets {
		if filter.Slice != nil && !sameSliceRef(cs.AuthoringSlice, filter.Slice) {
			continue
		}
		if len(filter.Statuses) > 0 && !slices.Contains(filter.Statuses, cs.Status) {
			continue
		}
		if filter.FilterAuthor && cs.Author != filter.AuthorSubjectID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(cs.Title), query) &&
			!strings.HasPrefix(cs.Id, "cs_"+strings.TrimPrefix(query, "cs_")) {
			continue
		}
		if filter.PageToken != "" && cs.Id <= filter.PageToken {
			continue
		}
		matched = append(matched, cs)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Id < matched[j].Id })
	nextToken := ""
	if len(matched) > limit {
		matched = matched[:limit]
		nextToken = matched[limit-1].Id
	}
	out := make([]*corev1.Changeset, 0, len(matched))
	for _, cs := range matched {
		clone := cloneChangeset(cs)
		storage.PopulateChangesetHandles(clone)
		if filter.Summary {
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
	return out, nextToken, nil
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
	if cs.Status != "draft" {
		return nil, storage.ErrConflict
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
	psAttr := s.b.resolveAttributionLocked(ctx, next.Author)
	if psAttr.ConversationID == "" && strings.TrimSpace(next.AuthoringConversationId) != "" {
		psAttr.ConversationID = strings.TrimSpace(next.AuthoringConversationId)
	}
	if next.Attribution != nil {
		explicit := storage.ActorAttributionFromProto(next.Attribution)
		if explicit.ActingSubjectID != "" {
			psAttr = explicit
		}
	}
	next.Attribution = psAttr.ToProto()
	cs.Patchsets = append(cs.Patchsets, next)
	cs.CurrentPatchsetId = next.Id
	cs.CurrentPatchsetNumber = next.Number
	cs.AffectedPaths = append([]string(nil), next.ChangedPaths...)
	cs.SubmitBlockedReason = ""
	if cs.Attribution != nil && cs.Attribution.ActingSubjectId == psAttr.ActingSubjectID {
		if cs.Attribution.DaemonId == "" && psAttr.DaemonID != "" {
			cs.Attribution.DaemonId = psAttr.DaemonID
		}
		if cs.Attribution.ConversationId == "" && psAttr.ConversationID != "" {
			cs.Attribution.ConversationId = psAttr.ConversationID
		}
	}
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
	if cs.Status != "draft" {
		return nil, storage.ErrConflict
	}
	subID := strings.TrimSpace(subjectID)
	key := patchsetRequirementKey(changesetID, patchset.Id)
	if s.b.approvals[key] == nil {
		s.b.approvals[key] = map[string]struct{}{}
	}
	s.b.approvals[key][subID] = struct{}{}
	var storedAttr *corev1.ActorAttribution
	for _, existing := range cs.Approvals {
		if existing.GetPatchsetId() == patchset.Id && existing.GetSubjectId() == subID {
			storedAttr = existing.GetAttribution()
			break
		}
	}
	if storedAttr == nil {
		attr := s.b.resolveAttributionLocked(ctx, subID)
		storedAttr = attr.ToProto()
		cs.Approvals = append(cs.Approvals, &corev1.ChangesetApproval{
			PatchsetId:  patchset.Id,
			SubjectId:   subID,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			Attribution: storedAttr,
		})
	}
	clearRequirementBlockLocked(cs, patchset.Id)
	return &corev1.ApproveChangesetResponse{
		ChangesetId: changesetID,
		PatchsetId:  patchset.Id,
		SubjectId:   subID,
		Attribution: cloneChangeset(&corev1.Changeset{Attribution: storedAttr}).GetAttribution(),
	}, nil
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
	if cs.Status != "draft" {
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
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) AddCollaborator(ctx context.Context, changesetID, subjectID, addedBy string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status != "draft" {
		return nil, fmt.Errorf("%w: collaborators can only be modified while a changeset is open; this one is %s", storage.ErrConflict, cs.Status)
	}
	if subjectID != strings.TrimSpace(cs.Author) && !slices.Contains(cs.Collaborators, subjectID) {
		cs.Collaborators = append(cs.Collaborators, subjectID)
	}
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) RemoveCollaborator(ctx context.Context, changesetID, subjectID string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status != "draft" {
		return nil, fmt.Errorf("%w: collaborators can only be modified while a changeset is open; this one is %s", storage.ErrConflict, cs.Status)
	}
	filtered := cs.Collaborators[:0]
	for _, existing := range cs.Collaborators {
		if strings.TrimSpace(existing) != subjectID {
			filtered = append(filtered, existing)
		}
	}
	if len(filtered) == 0 {
		cs.Collaborators = nil
	} else {
		cs.Collaborators = filtered
	}
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) Handoff(ctx context.Context, changesetID, newAuthorSubjectID string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	newAuthorSubjectID = strings.TrimSpace(newAuthorSubjectID)
	if newAuthorSubjectID == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status != "draft" {
		return nil, fmt.Errorf("%w: a changeset can only be handed off while it is open; this one is %s", storage.ErrConflict, cs.Status)
	}
	if newAuthorSubjectID != strings.TrimSpace(cs.Author) {
		if strings.TrimSpace(cs.OriginalAuthor) == "" {
			cs.OriginalAuthor = cs.Author
		}
		cs.Author = newAuthorSubjectID
		filtered := cs.Collaborators[:0]
		for _, existing := range cs.Collaborators {
			if strings.TrimSpace(existing) != newAuthorSubjectID {
				filtered = append(filtered, existing)
			}
		}
		if len(filtered) == 0 {
			cs.Collaborators = nil
		} else {
			cs.Collaborators = filtered
		}
	}
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
	return s.SubmitWithCheckStatuses(ctx, changesetID, expectedCurrentPatchsetID, nil)
}

func (s *ChangesetStore) SubmitWithCheckStatuses(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (res *corev1.SubmitChangesetResponse, err error) {
	return s.submit(ctx, changesetID, expectedCurrentPatchsetID, extraCheckStatuses, false)
}

// RecordSubmitBlockedReason records a service-enforced requirement block (such
// as path owners or .gitslice approval) onto a draft changeset.
func (s *ChangesetStore) RecordSubmitBlockedReason(_ context.Context, changesetID, expectedCurrentPatchsetID, reason string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if resolved := s.b.resolveChangesetSelectorLocked(changesetID); resolved != "" {
		changesetID = resolved
	}
	cs := s.b.changesets[changesetID]
	if cs != nil && cs.Status == "draft" && cs.CurrentPatchsetId == strings.TrimSpace(expectedCurrentPatchsetID) {
		cs.SubmitBlockedReason = reason
	}
	return nil
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
	if cs.Status == "abandoned" {
		return nil, storage.ErrConflict
	}
	if cs.Status == "submitted" && cs.CommitId != "" {
		return &corev1.SubmitChangesetResponse{CommitId: cs.CommitId, TargetRef: cs.TargetRef, NewRefCommitId: cs.CommitId, Status: "submitted"}, nil
	}
	if cs.Status == "pending_publish" {
		return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: cs.Status, PendingPublishId: cs.Id}, nil
	}
	if cs.Status != "draft" {
		return nil, storage.ErrConflict
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetId != expectedCurrentPatchsetID {
		return nil, storage.ErrConflict
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
	if slice == nil {
		return nil, storage.ErrNotFound
	}
	if s.b.retiredSlices[sliceID] {
		return nil, fmt.Errorf("%w: slice is deleted", storage.ErrConflict)
	}
	if slice.Archived {
		return nil, fmt.Errorf("%w: slice is archived", storage.ErrConflict)
	}
	latestReq := submitRequirementsForMemorySlice(slice)
	for _, p := range patchset.ChangedPaths {
		if slice.Definition == nil || !pathInAnyPrefix(slice.Definition.IncludedPaths, p) {
			return nil, s.b.blockSubmitLocked(cs, fmt.Sprintf("changed path %s is outside latest slice definition, refresh the changeset", p))
		}
	}
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
	if reason := storage.EvaluateSubmitRequirementsWithGroup(latestReq, excludedAuthors, approvers, checkStatuses, approverGroupMembers); reason != "" && !bypassRequirements {
		return nil, s.b.blockSubmitLocked(cs, reason)
	}
	submitAttr := s.b.resolveAttributionLocked(ctx, "")
	cs.Status = "pending_publish"
	cs.SubmitBlockedReason = ""
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
		s.b.clearSubmittedPathIntentsLocked(cs, patchset.ChangedPaths)
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
	if cs.Status != "draft" {
		return storage.ErrConflict
	}
	for _, child := range s.b.changesets {
		if child != nil && child.ParentChangesetId == changesetID && child.Status != "submitted" && child.Status != "abandoned" {
			return storage.ErrConflict
		}
	}
	cs.Status = "abandoned"
	s.b.clearAbandonedPathIntentsLocked(cs)
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

// ApprovalSubjects lists who approved a patchset.
func (s *ChangesetStore) ApprovalSubjects(ctx context.Context, changesetID, patchsetID string) ([]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []string
	for subjectID := range s.b.approvals[patchsetRequirementKey(changesetID, patchsetID)] {
		out = append(out, subjectID)
	}
	sort.Strings(out)
	return out, nil
}
