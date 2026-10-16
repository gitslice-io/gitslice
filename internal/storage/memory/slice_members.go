package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Slice members (design/26_slice_members.md) for the in-memory store.

type memSliceMember struct {
	role      string
	addedBy   string
	createdAt time.Time
}

func (s *AuthStore) SliceRole(ctx context.Context, subjectID, sliceID string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	member, ok := s.b.sliceMembers[strings.TrimSpace(sliceID)][strings.TrimSpace(subjectID)]
	if !ok {
		return "", storage.ErrNotFound
	}
	return member.role, nil
}

func (b *backend) sliceMemberLocked(sliceID, subjectID string, m memSliceMember) storage.SliceMember {
	username := b.personalAccounts[subjectID]
	kind := "user"
	if _, ok := b.agentRegistrations[subjectID]; ok {
		kind = "agent"
	}
	return storage.SliceMember{
		SliceID:     sliceID,
		SubjectID:   subjectID,
		Username:    username,
		DisplayName: b.profiles[username].DisplayName,
		Kind:        kind,
		Role:        m.role,
		AddedBy:     m.addedBy,
		CreatedAt:   m.createdAt,
	}
}

func (s *SliceStore) ListVisible(ctx context.Context, account, subjectID, after string, limit int, includeArchived bool) ([]*corev1.Slice, error) {
	return s.list(account, after, limit, true, strings.TrimSpace(subjectID), includeArchived)
}

func (s *SliceStore) ListMembers(ctx context.Context, sliceID string) ([]storage.SliceMember, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.SliceMember
	for subjectID, m := range s.b.sliceMembers[strings.TrimSpace(sliceID)] {
		out = append(out, s.b.sliceMemberLocked(sliceID, subjectID, m))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].SubjectID < out[j].SubjectID
	})
	return out, nil
}

func (s *SliceStore) SetMember(ctx context.Context, sliceID, subjectID, role, addedBy string) (*storage.SliceMember, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	sliceID, subjectID = strings.TrimSpace(sliceID), strings.TrimSpace(subjectID)
	if s.b.slices[sliceID] == nil {
		return nil, storage.ErrNotFound
	}
	members := s.b.sliceMembers[sliceID]
	if members == nil {
		members = map[string]memSliceMember{}
		s.b.sliceMembers[sliceID] = members
	}
	m, ok := members[subjectID]
	if !ok {
		m = memSliceMember{addedBy: addedBy, createdAt: time.Now().UTC()}
	}
	m.role = role
	members[subjectID] = m
	out := s.b.sliceMemberLocked(sliceID, subjectID, m)
	return &out, nil
}

func (s *SliceStore) RemoveMember(ctx context.Context, sliceID, subjectID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	members := s.b.sliceMembers[strings.TrimSpace(sliceID)]
	if _, ok := members[strings.TrimSpace(subjectID)]; !ok {
		return storage.ErrNotFound
	}
	delete(members, strings.TrimSpace(subjectID))
	return nil
}

func (s *SliceStore) ListShared(ctx context.Context, subjectID string) ([]storage.SharedSlice, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.SharedSlice
	for sliceID, members := range s.b.sliceMembers {
		m, ok := members[strings.TrimSpace(subjectID)]
		slice := s.b.slices[sliceID]
		if !ok || slice == nil || s.b.retiredSlices[sliceID] || slice.Archived {
			continue
		}
		out = append(out, storage.SharedSlice{Slice: cloneSlice(slice), Role: m.role})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Slice.GetRef(), out[j].Slice.GetRef()
		if a.GetAccount() != b.GetAccount() {
			return a.GetAccount() < b.GetAccount()
		}
		return a.GetSlice() < b.GetSlice()
	})
	return out, nil
}
