package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) CreateStack(ctx context.Context, subjectID string, req *corev1.CreateStackRequest) (*corev1.ChangesetStack, error) {
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stack := &corev1.ChangesetStack{
		Id:             s.b.nextIDLocked("stk"),
		AuthoringSlice: cloneSliceRef(req.AuthoringSlice),
		TargetRef:      targetRef,
		BaseCommitId:   baseCommitID,
		Title:          strings.TrimSpace(req.Title),
		Status:         "open",
		CreatedBy:      strings.TrimSpace(subjectID),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.b.stacks[stack.Id] = cloneStack(stack)
	return cloneStack(stack), nil
}

func (s *ChangesetStore) GetStack(ctx context.Context, stackID string) (*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(stackID)]
	if stack == nil {
		return nil, storage.ErrNotFound
	}
	return s.b.hydrateStackLocked(stack), nil
}

func (s *ChangesetStore) ListStacks(ctx context.Context, req *corev1.ListStacksRequest) ([]*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	limit := int(req.Limit)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	status := strings.TrimSpace(req.Status)
	out := make([]*corev1.ChangesetStack, 0, len(s.b.stacks))
	for _, stack := range s.b.stacks {
		if req.AuthoringSlice != nil && !sameSliceRef(stack.AuthoringSlice, req.AuthoringSlice) {
			continue
		}
		if status != "" && stack.Status != status {
			continue
		}
		if status == "" && stack.Status == "closed" {
			continue
		}
		out = append(out, s.b.hydrateStackLocked(stack))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].Id < out[j].Id
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *ChangesetStore) SetStackBaseCommit(ctx context.Context, stackID, baseCommitID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(stackID)]
	if stack == nil {
		return storage.ErrNotFound
	}
	baseCommitID = strings.TrimSpace(baseCommitID)
	if baseCommitID == "" {
		return storage.ErrInvalid
	}
	stack.BaseCommitId = baseCommitID
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return nil
}

func (s *ChangesetStore) SetStackStatus(ctx context.Context, stackID, stackStatus string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(stackID)]
	if stack == nil {
		return storage.ErrNotFound
	}
	stackStatus = strings.TrimSpace(stackStatus)
	if stackStatus == "" {
		return storage.ErrInvalid
	}
	stack.Status = stackStatus
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return nil
}

func (s *ChangesetStore) MoveStackEntry(ctx context.Context, req *corev1.MoveStackEntryRequest) (*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(req.StackId)]
	if stack == nil {
		return nil, storage.ErrNotFound
	}
	entry := stackEntryByChangeset(stack, strings.TrimSpace(req.ChangesetId))
	if entry == nil {
		return nil, storage.ErrNotFound
	}
	if cs := s.b.changesets[entry.ChangesetId]; cs == nil {
		return nil, storage.ErrNotFound
	} else if cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	s.b.reorderStackSiblingLocked(stack, entry, req.SiblingOrder)
	s.b.recomputeStackDisplayLocked(stack)
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.b.hydrateStackLocked(stack), nil
}

func (s *ChangesetStore) ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	stack := s.b.stacks[strings.TrimSpace(req.StackId)]
	if stack == nil {
		return nil, storage.ErrNotFound
	}
	entry := stackEntryByChangeset(stack, strings.TrimSpace(req.ChangesetId))
	if entry == nil {
		return nil, storage.ErrNotFound
	}
	cs := s.b.changesets[entry.ChangesetId]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}
	newParentID := strings.TrimSpace(req.NewParentChangesetId)
	newParentPatchsetID := strings.TrimSpace(req.NewParentPatchsetId)
	if newParentID == "" {
		if stack.RootEntryId != "" && stack.RootEntryId != entry.ChangesetId {
			return nil, storage.ErrConflict
		}
		newParentPatchsetID = ""
	} else {
		parentEntry := stackEntryByChangeset(stack, newParentID)
		parent := s.b.changesets[newParentID]
		if parentEntry == nil || parent == nil {
			return nil, storage.ErrInvalid
		}
		if s.b.stackEntryHasAncestorLocked(stack, newParentID, entry.ChangesetId) {
			return nil, storage.ErrConflict
		}
		if newParentPatchsetID == "" {
			newParentPatchsetID = parent.CurrentPatchsetId
		}
		if newParentPatchsetID == "" || newParentPatchsetID != parent.CurrentPatchsetId {
			return nil, storage.ErrConflict
		}
	}
	entry.ParentChangesetId = newParentID
	entry.ParentPatchsetId = newParentPatchsetID
	entry.State = "needs_restack"
	cs.ParentChangesetId = newParentID
	cs.ParentPatchsetId = newParentPatchsetID
	if newParentID == "" {
		cs.BaseKind = "commit"
		stack.RootEntryId = entry.ChangesetId
	} else {
		cs.BaseKind = "patchset"
	}
	s.b.markSubtreeStateLocked(stack, entry.ChangesetId, "needs_restack")
	s.b.reorderStackSiblingLocked(stack, entry, req.SiblingOrder)
	s.b.recomputeStackDisplayLocked(stack)
	stack.ActiveEntryId = entry.ChangesetId
	stack.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.b.hydrateStackLocked(stack), nil
}

func (s *ChangesetStore) DetachStackEntry(ctx context.Context, subjectID string, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	source := s.b.stacks[strings.TrimSpace(req.StackId)]
	if source == nil {
		return nil, storage.ErrNotFound
	}
	entry := stackEntryByChangeset(source, strings.TrimSpace(req.ChangesetId))
	if entry == nil {
		return nil, storage.ErrNotFound
	}
	if entry.ParentChangesetId == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[entry.ChangesetId]
	if cs == nil {
		return nil, storage.ErrNotFound
	}
	if cs.Status == "submitted" {
		return nil, storage.ErrConflict
	}

	descendants := s.b.stackSubtreeIDsLocked(source, entry.ChangesetId)
	for id := range descendants {
		child := s.b.changesets[id]
		if child == nil {
			return nil, storage.ErrNotFound
		}
		if child.Status == "submitted" {
			return nil, storage.ErrConflict
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(cs.Title)
	}
	if title == "" {
		title = strings.TrimSpace(source.Title)
	}
	if title == "" {
		title = "Detached stack"
	}
	detached := &corev1.ChangesetStack{
		Id:             s.b.nextIDLocked("stk"),
		AuthoringSlice: cloneSliceRef(source.AuthoringSlice),
		TargetRef:      source.TargetRef,
		BaseCommitId:   source.BaseCommitId,
		Title:          title,
		Status:         "open",
		ActiveEntryId:  entry.ChangesetId,
		RootEntryId:    entry.ChangesetId,
		CreatedBy:      strings.TrimSpace(subjectID),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	remaining := make([]*corev1.ChangesetStackEntry, 0, len(source.Entries)-len(descendants))
	for _, candidate := range source.Entries {
		if candidate == nil {
			continue
		}
		if _, ok := descendants[candidate.ChangesetId]; ok {
			next := cloneStackEntry(candidate)
			next.StackId = detached.Id
			next.State = "needs_restack"
			if next.ChangesetId == entry.ChangesetId {
				next.ParentChangesetId = ""
				next.ParentPatchsetId = ""
				next.SiblingOrder = 1
			}
			detached.Entries = append(detached.Entries, next)
			if child := s.b.changesets[next.ChangesetId]; child != nil {
				child.StackId = detached.Id
				child.ParentChangesetId = next.ParentChangesetId
				child.ParentPatchsetId = next.ParentPatchsetId
				if next.ChangesetId == entry.ChangesetId {
					child.BaseKind = "commit"
				}
			}
			continue
		}
		remaining = append(remaining, candidate)
	}
	source.Entries = remaining
	if _, ok := descendants[source.ActiveEntryId]; ok {
		source.ActiveEntryId = source.RootEntryId
	}
	source.UpdatedAt = now
	s.b.stacks[detached.Id] = detached
	s.b.recomputeStackDisplayLocked(source)
	s.b.recomputeStackDisplayLocked(detached)
	return &corev1.DetachStackEntryResponse{
		SourceStack:   s.b.hydrateStackLocked(source),
		DetachedStack: s.b.hydrateStackLocked(detached),
	}, nil
}
