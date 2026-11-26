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
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
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

func (s *ChangesetStore) AuthoringSlicesForTree(ctx context.Context, rootTreeID string) ([]*corev1.SliceRef, error) {
	if rootTreeID == "" {
		return nil, nil
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	seen := map[string]struct{}{}
	var out []*corev1.SliceRef
	for _, cs := range s.b.changesets {
		if cs == nil || cs.GetAuthoringSlice() == nil {
			continue
		}
		hasTree := false
		for _, ps := range cs.GetPatchsets() {
			if ps != nil && (strings.TrimSpace(ps.GetResultTreeId()) == rootTreeID || strings.TrimSpace(ps.GetBaseTreeId()) == rootTreeID) {
				hasTree = true
				break
			}
		}
		if !hasTree {
			continue
		}
		key := sliceRefKey(cs.GetAuthoringSlice())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cloneSliceRef(cs.GetAuthoringSlice()))
	}
	return out, nil
}

func (s *ChangesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
	}
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
	delete(s.b.submitBlockedCodes, cs.Id)
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
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
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
	s.b.clearRequirementBlockLocked(cs, patchset.Id)
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
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
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
	s.b.clearRequirementBlockLocked(cs, patchset.Id)
	return &corev1.ReportCheckResultResponse{ChangesetId: changesetID, PatchsetId: patchset.Id, CheckName: checkName, Status: resultStatus}, nil
}

// clearRequirementBlockLocked is the Postgres store's clearRequirementBlockTx.
func (b *backend) clearRequirementBlockLocked(cs *corev1.Changeset, patchsetID string) {
	current := currentPatchset(cs)
	if current == nil || current.Id != patchsetID {
		return
	}
	code := b.submitBlockedCodes[cs.Id]
	if code == storage.SubmitBlockCodeNone && cs.SubmitBlockedReason != "" {
		code = storage.ClassifySubmitBlockedReason(cs.SubmitBlockedReason)
	}
	if code.IsRequirementBlock() {
		cs.SubmitBlockedReason = ""
		delete(b.submitBlockedCodes, cs.Id)
	}
}

func (s *ChangesetStore) UpdateInfo(ctx context.Context, changesetID string, title, description *string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
	}
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
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(subjectID) == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	subjectID, err = storage.ValidateCollaboratorChange(cs.Status, subjectID)
	if err != nil {
		return nil, err
	}
	cs.Collaborators, _ = storage.ApplyCollaboratorAddition(cs.Author, cs.Collaborators, subjectID)
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) RemoveCollaborator(ctx context.Context, changesetID, subjectID string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(subjectID) == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	subjectID, err = storage.ValidateCollaboratorChange(cs.Status, subjectID)
	if err != nil {
		return nil, err
	}
	cs.Collaborators = storage.ApplyCollaboratorRemoval(cs.Collaborators, subjectID)
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) Handoff(ctx context.Context, changesetID, newAuthorSubjectID string) (*corev1.Changeset, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(newAuthorSubjectID) == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	newAuthorSubjectID, err = storage.ValidateHandoff(cs.Status, newAuthorSubjectID)
	if err != nil {
		return nil, err
	}
	cs.Author, cs.OriginalAuthor, cs.Collaborators, _ = storage.ApplyHandoff(cs.Author, cs.OriginalAuthor, cs.Collaborators, newAuthorSubjectID)
	out := cloneChangeset(cs)
	storage.PopulateChangesetHandles(out)
	return out, nil
}

func (s *ChangesetStore) Abandon(ctx context.Context, changesetID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var err error
	changesetID, err = s.b.resolveChangesetSelectorLocked(changesetID)
	if err != nil {
		return err
	}
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

func sortedChangesets(changesets map[string]*corev1.Changeset) []*corev1.Changeset {
	out := make([]*corev1.Changeset, 0, len(changesets))
	for _, cs := range changesets {
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
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
	if resolved, err := s.b.resolveChangesetSelectorLocked(changesetID); err == nil {
		changesetID = resolved
	}
	var out []string
	for subjectID := range s.b.approvals[patchsetRequirementKey(changesetID, patchsetID)] {
		out = append(out, subjectID)
	}
	sort.Strings(out)
	return out, nil
}
