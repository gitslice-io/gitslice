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

func (s *SliceStore) Create(ctx context.Context, subjectID string, ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.sliceRefs[sliceRefKey(ref)]; ok {
		return nil, storage.ErrConflict
	}
	if _, ok := s.b.sliceAliases[sliceRefKey(ref)]; ok {
		return nil, fmt.Errorf("%w: slice name %s is reserved: a slice was renamed from it", storage.ErrConflict, sliceRefKey(ref))
	}
	return s.b.putSliceLocked(ref, includedPaths, visibility, requiredApprovals, requiredChecks, subjectID)
}

func (s *SliceStore) ValidateDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error) {
	return validateSliceDefinition(ref, includedPaths, visibility, requiredApprovals, requiredChecks)
}

func (s *SliceStore) Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	id := s.b.sliceRefs[sliceRefKey(ref)]
	if id == "" {
		// A renamed slice still answers to its earlier names.
		id = s.b.sliceAliases[sliceRefKey(ref)]
	}
	if id == "" || s.b.retiredSlices[id] {
		return nil, storage.ErrNotFound
	}
	return s.withCIDaemonName(cloneSlice(s.b.slices[id])), nil
}

func (s *SliceStore) Get(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil {
		return nil, storage.ErrNotFound
	}
	return s.withCIDaemonName(cloneSlice(slice)), nil
}

func (s *SliceStore) List(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error) {
	return s.list(account, after, limit, false, "", includeArchived)
}

func (s *SliceStore) ListPublic(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error) {
	return s.list(account, after, limit, true, "", includeArchived)
}

// list lists an account's slices: all of them, or with publicOnly the public
// ones and those shared with sharedWith.
func (s *SliceStore) list(account, after string, limit int, publicOnly bool, sharedWith string, includeArchived bool) ([]*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	var out []*corev1.Slice
	for _, slice := range s.b.slices {
		if s.b.retiredSlices[slice.Id] || (slice.Archived && !includeArchived) {
			continue
		}
		_, shared := s.b.sliceMembers[slice.Id][sharedWith]
		if slice.Ref.Account == account && slice.Ref.Slice > after && (!publicOnly || slice.GetDefinition().GetVisibility() == "public" || (sharedWith != "" && shared)) {
			out = append(out, s.withCIDaemonName(cloneSlice(slice)))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Slice < out[j].Ref.Slice })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SliceStore) ListDefinitionVersions(ctx context.Context, sliceID string, limit int) ([]*corev1.SliceDefinitionVersion, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.slices[sliceID] == nil {
		return nil, storage.ErrNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	versions := s.b.sliceDefinitionVersions[sliceID]
	out := make([]*corev1.SliceDefinitionVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, cloneSliceDefinitionVersion(versions[i]))
	}
	return out, nil
}

func (s *SliceStore) UpdateDefinition(ctx context.Context, subjectID, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	current := s.b.slices[sliceID]
	if current == nil {
		return nil, storage.ErrNotFound
	}
	if expectedHash != "" && current.DefinitionHash != expectedHash {
		return nil, storage.ErrConflict
	}
	included, visibility, requiredApprovals, requiredChecks, err := validateSliceDefinition(current.Ref, definition.IncludedPaths, definition.Visibility, definition.RequiredApprovals, definition.RequiredChecks)
	if err != nil {
		return nil, err
	}
	current.Definition.Version++
	current.Definition.IncludedPaths = included
	current.Definition.Visibility = visibility
	current.Definition.RequiredApprovals = requiredApprovals
	current.Definition.RequiredChecks = append([]string(nil), requiredChecks...)
	current.DefinitionHash = memoryDefinitionHash(sliceID, current.Definition.Version, included, visibility, requiredApprovals, requiredChecks)
	s.b.appendSliceDefinitionVersionLocked(current, subjectID)
	return cloneSlice(current).Definition, nil
}

func (s *SliceStore) SetCIDaemon(ctx context.Context, sliceID, daemonID string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	current := s.b.slices[strings.TrimSpace(sliceID)]
	if current == nil {
		return nil, storage.ErrNotFound
	}
	current.CiDaemonId = strings.TrimSpace(daemonID)
	return s.withCIDaemonName(cloneSlice(current)), nil
}

func (s *SliceStore) ListByCIDaemon(ctx context.Context, daemonIDs []string) ([]*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	wanted := map[string]bool{}
	for _, id := range daemonIDs {
		wanted[id] = true
	}
	var out []*corev1.Slice
	for _, slice := range s.b.slices {
		if wanted[slice.CiDaemonId] && !s.b.retiredSlices[slice.Id] {
			out = append(out, s.withCIDaemonName(cloneSlice(slice)))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref.Account != out[j].Ref.Account {
			return out[i].Ref.Account < out[j].Ref.Account
		}
		return out[i].Ref.Slice < out[j].Ref.Slice
	})
	return out, nil
}

// withCIDaemonName fills in the name the slice's CI daemon is shown by. The
// caller holds the lock.
func (s *SliceStore) withCIDaemonName(slice *corev1.Slice) *corev1.Slice {
	if d := s.b.agentDaemons[slice.CiDaemonId]; d != nil {
		slice.CiDaemonName = d.DisplayName
		if slice.CiDaemonName == "" {
			slice.CiDaemonName = d.Name
		}
	}
	return slice
}

func (s *SliceStore) Delete(ctx context.Context, sliceID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[sliceID]
	if slice == nil {
		return storage.ErrNotFound
	}
	delete(s.b.sliceRefs, sliceRefKey(slice.Ref))
	for key, id := range s.b.sliceAliases {
		if id == sliceID {
			delete(s.b.sliceAliases, key)
		}
	}
	delete(s.b.slices, sliceID)
	delete(s.b.sliceDefinitionVersions, sliceID)
	s.b.unscopeSecretsLocked(sliceID)
	return nil
}

func (s *SliceStore) CoveringIDsByPath(ctx context.Context, changedPaths []string) (map[string][]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	byPrefix := map[string][]string{}
	for _, slice := range s.b.slices {
		if s.b.retiredSlices[slice.Id] {
			continue
		}
		for _, prefix := range slice.Definition.IncludedPaths {
			byPrefix[prefix] = append(byPrefix[prefix], slice.Id)
		}
	}
	return storage.AssembleCoverageByPath(changedPaths, byPrefix), nil
}

func (s *SliceStore) CreateTag(ctx context.Context, tag storage.SliceTag) (*storage.SliceTag, bool, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.sliceTags[tag.SliceID] == nil {
		s.b.sliceTags[tag.SliceID] = map[string]storage.SliceTag{}
	}
	if existing, ok := s.b.sliceTags[tag.SliceID][tag.Name]; ok {
		if existing.CommitID != tag.CommitID {
			return &existing, false, fmt.Errorf("%w: tag %q already names commit %s", storage.ErrConflict, tag.Name, existing.CommitID)
		}
		return &existing, false, nil
	}
	tag.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.b.sliceTags[tag.SliceID][tag.Name] = tag
	return &tag, true, nil
}

func (s *SliceStore) ListTags(ctx context.Context, sliceID string) ([]storage.SliceTag, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := make([]storage.SliceTag, 0, len(s.b.sliceTags[sliceID]))
	for _, tag := range s.b.sliceTags[sliceID] {
		out = append(out, tag)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
