package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

type memAccountGroup struct {
	id          string
	account     string
	name        string
	description string
	createdBy   string
	createdAt   time.Time
	updatedAt   time.Time
	members     map[string]memAccountGroupMember
}

type memAccountGroupMember struct {
	addedBy   string
	createdAt time.Time
}

type memSliceGroupMember struct {
	role      string
	addedBy   string
	createdAt time.Time
}

func groupKey(account, name string) string {
	return account + "\x00" + name
}

func (b *backend) accountExistsLocked(accountSlug string) bool {
	return b.orgAccounts[accountSlug] || b.accountSlugTakenLocked(accountSlug)
}

func (b *backend) subjectUsernameLocked(subjectID string) string {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return ""
	}
	if u := b.personalAccounts[subjectID]; u != "" {
		return u
	}
	return subjectID
}

func (b *backend) accountGroupProtoLocked(g *memAccountGroup) *storage.AccountGroup {
	members := make([]storage.AccountGroupMember, 0, len(g.members))
	for subjectID, m := range g.members {
		username := b.personalAccounts[subjectID]
		subj := b.subjects[subjectID]
		kind := subj.Kind
		if kind == "" {
			if _, ok := b.agentRegistrations[subjectID]; ok {
				kind = "agent"
			} else {
				kind = "user"
			}
		}
		displayName := b.profiles[username].DisplayName
		if displayName == "" && subj.DisplayName != "" && subj.DisplayName != subjectID {
			displayName = subj.DisplayName
		}
		members = append(members, storage.AccountGroupMember{
			SubjectID:   subjectID,
			Username:    username,
			DisplayName: displayName,
			Kind:        kind,
			AddedBy:     b.subjectUsernameLocked(m.addedBy),
			CreatedAt:   m.createdAt,
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if !members[i].CreatedAt.Equal(members[j].CreatedAt) {
			return members[i].CreatedAt.Before(members[j].CreatedAt)
		}
		return members[i].SubjectID < members[j].SubjectID
	})
	return &storage.AccountGroup{
		ID:          g.id,
		Account:     g.account,
		Name:        g.name,
		Handle:      storage.FormatGroupHandle(g.account, g.name),
		Description: g.description,
		CreatedBy:   b.subjectUsernameLocked(g.createdBy),
		CreatedAt:   g.createdAt,
		UpdatedAt:   g.updatedAt,
		Members:     members,
		MemberCount: int32(len(members)),
	}
}

func (b *backend) groupMemberSubjectIDsLocked(groupRef string) map[string]bool {
	account, name, ok := storage.ParseGroupRef(groupRef)
	if !ok {
		return nil
	}
	gid := b.accountGroupKeys[groupKey(account, name)]
	g := b.accountGroups[gid]
	if g == nil {
		return nil
	}
	out := make(map[string]bool, len(g.members))
	for id := range g.members {
		out[id] = true
	}
	return out
}

func (b *backend) subjectSliceGroupRoleLocked(subjectID, sliceID string) (string, bool) {
	groups := b.sliceGroupMembers[strings.TrimSpace(sliceID)]
	if len(groups) == 0 {
		return "", false
	}
	var best string
	bestRank := len(storage.SliceRoles)
	for gid, m := range groups {
		g := b.accountGroups[gid]
		if g == nil {
			continue
		}
		if _, ok := g.members[strings.TrimSpace(subjectID)]; !ok {
			continue
		}
		rank := storage.SliceRoleRank(m.role)
		if rank < bestRank {
			bestRank = rank
			best = m.role
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func (s *AuthStore) CreateGroup(ctx context.Context, accountSlug, name, description, createdBySubjectID string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	createdBySubjectID = strings.TrimSpace(createdBySubjectID)

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if !s.b.accountExistsLocked(accountSlug) {
		return nil, storage.ErrNotFound
	}
	k := groupKey(accountSlug, name)
	if _, exists := s.b.accountGroupKeys[k]; exists {
		return nil, fmt.Errorf("%w: group %s already exists", storage.ErrConflict, storage.FormatGroupHandle(accountSlug, name))
	}
	id, err := objectid.RandomID("grp")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	g := &memAccountGroup{
		id:          id,
		account:     accountSlug,
		name:        name,
		description: description,
		createdBy:   createdBySubjectID,
		createdAt:   now,
		updatedAt:   now,
		members:     map[string]memAccountGroupMember{},
	}
	s.b.accountGroups[id] = g
	s.b.accountGroupKeys[k] = id
	return s.b.accountGroupProtoLocked(g), nil
}

func (s *AuthStore) GetGroup(ctx context.Context, accountSlug, name string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	gid := s.b.accountGroupKeys[groupKey(accountSlug, name)]
	g := s.b.accountGroups[gid]
	if g == nil {
		return nil, storage.ErrNotFound
	}
	return s.b.accountGroupProtoLocked(g), nil
}

func (s *AuthStore) ListGroups(ctx context.Context, accountSlug string) ([]storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if !s.b.accountExistsLocked(accountSlug) {
		return nil, storage.ErrNotFound
	}
	var out []storage.AccountGroup
	for _, g := range s.b.accountGroups {
		if g != nil && g.account == accountSlug {
			out = append(out, *s.b.accountGroupProtoLocked(g))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *AuthStore) DeleteGroup(ctx context.Context, accountSlug, name string) error {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	k := groupKey(accountSlug, name)
	gid := s.b.accountGroupKeys[k]
	if gid == "" {
		return storage.ErrNotFound
	}
	delete(s.b.accountGroupKeys, k)
	delete(s.b.accountGroups, gid)
	for _, sliceGroups := range s.b.sliceGroupMembers {
		delete(sliceGroups, gid)
	}
	return nil
}

func (s *AuthStore) AddGroupMember(ctx context.Context, accountSlug, name, subjectID, addedBySubjectID string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	addedBySubjectID = strings.TrimSpace(addedBySubjectID)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", storage.ErrInvalid)
	}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	gid := s.b.accountGroupKeys[groupKey(accountSlug, name)]
	g := s.b.accountGroups[gid]
	if g == nil {
		return nil, storage.ErrNotFound
	}
	if _, ok := s.b.subjects[subjectID]; !ok {
		return nil, storage.ErrNotFound
	}
	if _, exists := g.members[subjectID]; !exists {
		g.members[subjectID] = memAccountGroupMember{
			addedBy:   addedBySubjectID,
			createdAt: time.Now().UTC(),
		}
		g.updatedAt = time.Now().UTC()
	}
	return s.b.accountGroupProtoLocked(g), nil
}

func (s *AuthStore) RemoveGroupMember(ctx context.Context, accountSlug, name, subjectID string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", storage.ErrInvalid)
	}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	gid := s.b.accountGroupKeys[groupKey(accountSlug, name)]
	g := s.b.accountGroups[gid]
	if g == nil {
		return nil, storage.ErrNotFound
	}
	if _, exists := g.members[subjectID]; !exists {
		return nil, storage.ErrNotFound
	}
	delete(g.members, subjectID)
	g.updatedAt = time.Now().UTC()
	return s.b.accountGroupProtoLocked(g), nil
}

func (s *AuthStore) SliceGroupRole(ctx context.Context, subjectID, sliceID string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	role, ok := s.b.subjectSliceGroupRoleLocked(subjectID, sliceID)
	if !ok {
		return "", storage.ErrNotFound
	}
	return role, nil
}
