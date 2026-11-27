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
	return s.CreateDefinition(ctx, subjectID, ref, &corev1.SliceDefinition{
		IncludedPaths:     includedPaths,
		Visibility:        visibility,
		RequiredApprovals: requiredApprovals,
		RequiredChecks:    requiredChecks,
	})
}

func (s *SliceStore) CreateDefinition(ctx context.Context, subjectID string, ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.Slice, error) {
	if definition == nil {
		return nil, storage.ErrInvalid
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.sliceRefs[sliceRefKey(ref)]; ok {
		return nil, storage.ErrConflict
	}
	if _, ok := s.b.sliceAliases[sliceRefKey(ref)]; ok {
		return nil, fmt.Errorf("%w: slice name %s is reserved: a slice was renamed from it", storage.ErrConflict, sliceRefKey(ref))
	}
	return s.b.putSliceLocked(ref, definition.IncludedPaths, definition.Visibility, definition.RequiredApprovals, definition.RequiredHumanApprovals, definition.RequiredChecks, definition.Approvers, subjectID)
}

func (s *SliceStore) ValidateSliceDefinition(ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	return validateSliceDefinitionProto(ref, definition)
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

func (s *SliceStore) ListSorted(ctx context.Context, account, visibilityMode, subjectID, afterCursor string, limit int, includeArchived bool, sortOrder storage.SliceSort) ([]*corev1.Slice, string, error) {
	publicOnly := visibilityMode != "all"
	sharedWith := ""
	if visibilityMode == "visible" {
		sharedWith = strings.TrimSpace(subjectID)
	}
	if limit <= 0 {
		limit = 50
	}
	if sortOrder == "" || sortOrder == storage.SliceSortName {
		slices, err := s.list(account, afterCursor, limit+1, publicOnly, sharedWith, includeArchived)
		if err != nil {
			return nil, "", err
		}
		var next string
		if len(slices) > limit {
			slices = slices[:limit]
			next = slices[limit-1].GetRef().GetSlice()
		}
		return slices, next, nil
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var items []*corev1.Slice
	for _, slice := range s.b.slices {
		if s.b.retiredSlices[slice.Id] || (slice.Archived && !includeArchived) {
			continue
		}
		_, shared := s.b.sliceMembers[slice.Id][sharedWith]
		if !shared && sharedWith != "" {
			_, shared = s.b.subjectSliceGroupRoleLocked(sharedWith, slice.Id)
		}
		if slice.Ref.Account == account && (!publicOnly || slice.GetDefinition().GetVisibility() == "public" || (sharedWith != "" && shared)) {
			items = append(items, s.withCIDaemonName(cloneSlice(slice)))
		}
	}
	sortTimestamp := func(sl *corev1.Slice) int64 {
		raw := sl.GetCreatedAt()
		if sortOrder == storage.SliceSortActive && sl.GetLastActivityAt() != "" {
			raw = sl.GetLastActivityAt()
		}
		if raw == "" {
			return 0
		}
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return 0
		}
		return t.UTC().UnixNano()
	}
	sort.Slice(items, func(i, j int) bool {
		hi := items[i].GetRef().GetSlice() == "home"
		hj := items[j].GetRef().GetSlice() == "home"
		if hi != hj {
			return hi
		}
		ti, tj := sortTimestamp(items[i]), sortTimestamp(items[j])
		if ti != tj {
			return ti > tj
		}
		return items[i].GetRef().GetSlice() < items[j].GetRef().GetSlice()
	})
	start := 0
	if afterCursor != "" {
		found := false
		for i, sl := range items {
			if sl.GetRef().GetSlice() == afterCursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, "", nil
		}
	}
	if start >= len(items) {
		return nil, "", nil
	}
	items = items[start:]
	var next string
	if len(items) > limit {
		items = items[:limit]
		next = items[limit-1].GetRef().GetSlice()
	}
	return items, next, nil
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
		if !shared && sharedWith != "" {
			_, shared = s.b.subjectSliceGroupRoleLocked(sharedWith, slice.Id)
		}
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
	validated, err := validateSliceDefinitionProto(current.Ref, definition)
	if err != nil {
		return nil, err
	}
	current.Definition.Version++
	current.Definition.IncludedPaths = validated.IncludedPaths
	current.Definition.Visibility = validated.Visibility
	current.Definition.RequiredApprovals = validated.RequiredApprovals
	current.Definition.RequiredHumanApprovals = validated.RequiredHumanApprovals
	current.Definition.RequiredChecks = append([]string(nil), validated.RequiredChecks...)
	current.Definition.Approvers = validated.Approvers
	current.DefinitionHash = memoryDefinitionHash(sliceID, current.Definition.Version, validated.IncludedPaths, validated.Visibility, validated.RequiredApprovals, validated.RequiredHumanApprovals, validated.RequiredChecks, validated.Approvers)
	s.b.appendSliceDefinitionVersionLocked(current, subjectID)
	return cloneSlice(current).Definition, nil
}

func (s *SliceStore) SetCIDaemons(ctx context.Context, sliceID string, daemonIDs []string) (*corev1.Slice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	current := s.b.slices[strings.TrimSpace(sliceID)]
	if current == nil {
		return nil, storage.ErrNotFound
	}
	daemonIDs = storage.NormalizeCIDaemons(daemonIDs)
	current.CiDaemonIds = daemonIDs
	current.CiDaemonId = ""
	if len(daemonIDs) > 0 {
		current.CiDaemonId = daemonIDs[0]
	}
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
		inPool := false
		for _, id := range storage.CIDaemonPool(slice) {
			inPool = inPool || wanted[id]
		}
		if inPool && !s.b.retiredSlices[slice.Id] {
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

func (s *SliceStore) AccountCIDaemons(ctx context.Context, account string) ([]string, []string, error) {
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" {
		return nil, nil, fmt.Errorf("%w: account is required", storage.ErrInvalid)
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	pool := append([]string(nil), s.b.accountCIDaemons[account]...)
	names := make([]string, len(pool))
	for i, id := range pool {
		names[i] = s.daemonNameLocked(id)
	}
	return pool, names, nil
}

func (s *SliceStore) SetAccountCIDaemons(ctx context.Context, account string, daemonIDs []string) ([]string, []string, error) {
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" {
		return nil, nil, fmt.Errorf("%w: account is required", storage.ErrInvalid)
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if !s.accountExistsLocked(account) {
		return nil, nil, storage.ErrNotFound
	}
	pool := storage.NormalizeCIDaemons(daemonIDs)
	if len(pool) == 0 {
		delete(s.b.accountCIDaemons, account)
	} else {
		s.b.accountCIDaemons[account] = pool
	}
	names := make([]string, len(pool))
	for i, id := range pool {
		names[i] = s.daemonNameLocked(id)
	}
	return append([]string(nil), pool...), names, nil
}

func (s *SliceStore) ListAccountsByCIDaemon(ctx context.Context, daemonIDs []string) (map[string][]string, error) {
	if len(daemonIDs) == 0 {
		return nil, nil
	}
	wanted := map[string]bool{}
	for _, id := range daemonIDs {
		wanted[id] = true
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := map[string][]string{}
	for account, pool := range s.b.accountCIDaemons {
		for _, id := range pool {
			if wanted[id] {
				out[id] = append(out[id], account)
			}
		}
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out, nil
}

func (s *SliceStore) accountExistsLocked(account string) bool {
	if s.b.orgAccounts[account] {
		return true
	}
	if _, ok := s.b.accountMembers[account]; ok {
		return true
	}
	for _, slug := range s.b.personalAccounts {
		if slug == account {
			return true
		}
	}
	for _, slice := range s.b.slices {
		if slice != nil && slice.Ref != nil && strings.EqualFold(slice.Ref.Account, account) {
			return true
		}
	}
	return false
}

func (s *SliceStore) daemonNameLocked(id string) string {
	if d := s.b.agentDaemons[id]; d != nil {
		if d.DisplayName != "" {
			return d.DisplayName
		}
		if d.Name != "" {
			return d.Name
		}
	}
	return id
}

// withCIDaemonName fills in the name the slice's CI daemon is shown by. The
// caller holds the lock.
func (s *SliceStore) withCIDaemonName(slice *corev1.Slice) *corev1.Slice {
	name := func(id string) string {
		if d := s.b.agentDaemons[id]; d != nil {
			if d.DisplayName != "" {
				return d.DisplayName
			}
			return d.Name
		}
		return ""
	}
	slice.CiDaemonName = name(slice.CiDaemonId)
	slice.CiDaemonIds = storage.CIDaemonPool(slice)
	slice.CiDaemonNames = nil
	for _, id := range slice.CiDaemonIds {
		member := name(id)
		if member == "" {
			member = id
		}
		slice.CiDaemonNames = append(slice.CiDaemonNames, member)
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
	delete(s.b.sliceGroupMembers, sliceID)
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
	if tag.Attribution.ActingSubjectID == "" {
		tag.Attribution = s.b.resolveAttributionLocked(ctx, tag.CreatedBy)
	} else {
		tag.Attribution = tag.Attribution.Normalize()
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
		if cmp := compareMemoryTagTimesDesc(out[i].CreatedAt, out[j].CreatedAt); cmp != 0 {
			return cmp < 0
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *SliceStore) ListAccountTags(ctx context.Context, account string, publicOnly bool, sharedWith string) ([]storage.AccountSliceTag, error) {
	account = strings.TrimSpace(account)
	sharedWith = strings.TrimSpace(sharedWith)
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.AccountSliceTag
	for _, slice := range s.b.slices {
		if slice.GetRef().GetAccount() != account || s.b.retiredSlices[slice.Id] {
			continue
		}
		visibility := slice.GetDefinition().GetVisibility()
		if publicOnly && visibility != "public" {
			if sharedWith == "" {
				continue
			}
			_, shared := s.b.sliceMembers[slice.Id][sharedWith]
			if !shared {
				_, shared = s.b.subjectSliceGroupRoleLocked(sharedWith, slice.Id)
			}
			if !shared {
				continue
			}
		}
		for _, tag := range s.b.sliceTags[slice.Id] {
			out = append(out, storage.AccountSliceTag{
				SliceTag:   tag,
				SliceSlug:  slice.GetRef().GetSlice(),
				Visibility: visibility,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if cmp := compareMemoryTagTimesDesc(out[i].CreatedAt, out[j].CreatedAt); cmp != 0 {
			return cmp < 0
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].SliceSlug < out[j].SliceSlug
	})
	return out, nil
}

func compareMemoryTagTimesDesc(a, b string) int {
	ta, ea := time.Parse(time.RFC3339Nano, a)
	tb, eb := time.Parse(time.RFC3339Nano, b)
	if ea == nil && eb == nil {
		if ta.After(tb) {
			return -1
		}
		if tb.After(ta) {
			return 1
		}
		return 0
	}
	if a > b {
		return -1
	}
	if a < b {
		return 1
	}
	return 0
}
