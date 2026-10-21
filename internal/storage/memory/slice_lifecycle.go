package memory

import (
	"context"
	"fmt"
	"sort"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Rename gives the slice a new name; see storage.SliceStore.
func (s *SliceStore) Rename(ctx context.Context, sliceID, newName string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil || s.b.retiredSlices[sliceID] {
		return nil, storage.ErrNotFound
	}
	next, err := normalizeSliceRef(&corev1.SliceRef{Account: slice.Ref.Account, Slice: newName})
	if err != nil {
		return nil, err
	}
	oldKey, newKey := sliceRefKey(slice.Ref), sliceRefKey(next)
	if oldKey == newKey {
		return nil, fmt.Errorf("%w: slice %s already has that name", storage.ErrInvalid, oldKey)
	}
	if _, taken := s.b.sliceRefs[newKey]; taken {
		return nil, fmt.Errorf("%w: slice %s already exists", storage.ErrConflict, newKey)
	}
	if owner, reserved := s.b.sliceAliases[newKey]; reserved {
		if owner != sliceID {
			return nil, fmt.Errorf("%w: slice name %s is reserved: another slice was renamed from it", storage.ErrConflict, newKey)
		}
		delete(s.b.sliceAliases, newKey) // taking back one of its own earlier names
	}
	old := cloneSliceRef(slice.Ref)
	delete(s.b.sliceRefs, oldKey)
	s.b.sliceRefs[newKey] = sliceID
	s.b.sliceAliases[oldKey] = sliceID
	slice.Ref = next
	slice.FormerNames = s.b.formerNamesLocked(sliceID)
	for _, cs := range s.b.changesets {
		if sameSliceRef(cs.AuthoringSlice, old) {
			cs.AuthoringSlice = cloneSliceRef(next)
		}
	}
	for _, stack := range s.b.stacks {
		if sameSliceRef(stack.AuthoringSlice, old) {
			stack.AuthoringSlice = cloneSliceRef(next)
		}
	}
	return cloneSlice(slice), nil
}

// formerNamesLocked lists the slice's earlier names, which still resolve to it.
func (b *backend) formerNamesLocked(sliceID string) []string {
	var names []string
	for key, id := range b.sliceAliases {
		if id == sliceID {
			names = append(names, aliasName(key))
		}
	}
	sort.Strings(names)
	return names
}

// aliasName returns the slice part of an "account/slice" key.
func aliasName(key string) string {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[i+1:]
		}
	}
	return key
}

// SetDescription sets the slice's description; see storage.SliceStore.
func (s *SliceStore) SetDescription(ctx context.Context, sliceID, description string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil || s.b.retiredSlices[sliceID] {
		return nil, storage.ErrNotFound
	}
	slice.Description = description
	return cloneSlice(slice), nil
}

// SetMergeOnSubmit turns merging stale changesets on submit on or off.
func (s *SliceStore) SetMergeOnSubmit(ctx context.Context, sliceID string, enabled bool) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil || s.b.retiredSlices[sliceID] {
		return nil, storage.ErrNotFound
	}
	slice.MergeOnSubmit = enabled
	return cloneSlice(slice), nil
}

// SetArchived archives the slice or restores it; see storage.SliceStore.
func (s *SliceStore) SetArchived(ctx context.Context, sliceID string, archived bool) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil || s.b.retiredSlices[sliceID] {
		return nil, storage.ErrNotFound
	}
	slice.Archived = archived
	return cloneSlice(slice), nil
}

// ChangesetCounts returns how many changesets the slice authored and how many
// are still open.
func (s *SliceStore) ChangesetCounts(ctx context.Context, sliceID string) (total, open int, err error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil {
		return 0, 0, storage.ErrNotFound
	}
	for _, cs := range s.b.changesets {
		if !sameSliceRef(cs.AuthoringSlice, slice.Ref) {
			continue
		}
		total++
		if cs.Status != "submitted" && cs.Status != "abandoned" {
			open++
		}
	}
	return total, open, nil
}

// Retire removes a slice that has changesets; see storage.SliceStore.
func (s *SliceStore) Retire(ctx context.Context, sliceID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil || s.b.retiredSlices[sliceID] {
		return storage.ErrNotFound
	}
	for _, cs := range s.b.changesets {
		if sameSliceRef(cs.AuthoringSlice, slice.Ref) && cs.Status != "submitted" && cs.Status != "abandoned" {
			return fmt.Errorf("%w: slice has an open changeset", storage.ErrConflict)
		}
	}
	s.b.retiredSlices[sliceID] = true
	delete(s.b.sliceSecrets, sliceID)
	return nil
}

// ResolveRetired finds a retired slice by the name it had.
func (s *SliceStore) ResolveRetired(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	id := s.b.sliceRefs[sliceRefKey(ref)]
	if id == "" || !s.b.retiredSlices[id] {
		return nil, storage.ErrNotFound
	}
	return cloneSlice(s.b.slices[id]), nil
}
