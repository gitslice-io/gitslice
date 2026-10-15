package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

func (s *AuthStore) CountOrganizationsCreatedBy(ctx context.Context, subjectID string) (int, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	count := 0
	for _, creator := range s.b.orgCreators {
		if creator == strings.TrimSpace(subjectID) {
			count++
		}
	}
	return count, nil
}

func (s *AuthStore) UpsertAccountInvitation(ctx context.Context, accountSlug, subjectID, role, invitedBy string) (*storage.AccountInvitation, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	accountSlug = strings.TrimSpace(accountSlug)
	if err := s.b.requireOrgLocked(accountSlug); err != nil {
		return nil, err
	}
	if s.b.invitations[accountSlug] == nil {
		s.b.invitations[accountSlug] = map[string]storage.AccountInvitation{}
	}
	invitation := storage.AccountInvitation{Account: accountSlug, SubjectID: subjectID, Role: role, InvitedBySubjectID: invitedBy, CreatedAt: time.Now().UTC()}
	s.b.invitations[accountSlug][subjectID] = invitation
	return &invitation, nil
}

func sortInvitations(out []storage.AccountInvitation) {
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return out[i].SubjectID < out[j].SubjectID
	})
}

func (s *AuthStore) ListAccountInvitations(ctx context.Context, accountSlug string) ([]storage.AccountInvitation, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.AccountInvitation
	for _, invitation := range s.b.invitations[strings.TrimSpace(accountSlug)] {
		out = append(out, invitation)
	}
	sortInvitations(out)
	return out, nil
}

func (s *AuthStore) ListSubjectInvitations(ctx context.Context, subjectID string) ([]storage.AccountInvitation, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.AccountInvitation
	for _, byAccount := range s.b.invitations {
		if invitation, ok := byAccount[strings.TrimSpace(subjectID)]; ok {
			out = append(out, invitation)
		}
	}
	sortInvitations(out)
	return out, nil
}

func (s *AuthStore) GetAccountInvitation(ctx context.Context, accountSlug, subjectID string) (*storage.AccountInvitation, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	invitation, ok := s.b.invitations[strings.TrimSpace(accountSlug)][strings.TrimSpace(subjectID)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &invitation, nil
}

func (s *AuthStore) AcceptAccountInvitation(ctx context.Context, accountSlug, subjectID string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	accountSlug = strings.TrimSpace(accountSlug)
	invitation, ok := s.b.invitations[accountSlug][subjectID]
	if !ok {
		return "", storage.ErrNotFound
	}
	delete(s.b.invitations[accountSlug], subjectID)
	if s.b.accountMembers[subjectID] != nil {
		delete(s.b.accountMembers[subjectID], accountSlug)
	}
	s.b.addAccountRoleLocked(subjectID, accountSlug, invitation.Role)
	return invitation.Role, nil
}

func (s *AuthStore) DeleteAccountInvitation(ctx context.Context, accountSlug, subjectID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	accountSlug = strings.TrimSpace(accountSlug)
	if _, ok := s.b.invitations[accountSlug][subjectID]; !ok {
		return storage.ErrNotFound
	}
	delete(s.b.invitations[accountSlug], subjectID)
	return nil
}

func (s *AuthStore) GetAccountProfile(ctx context.Context, accountSlug string) (*storage.AccountProfile, error) {
	kind, err := s.AccountKind(ctx, accountSlug)
	if err != nil {
		return nil, err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	profile := s.b.profiles[strings.TrimSpace(accountSlug)]
	profile.Account = strings.TrimSpace(accountSlug)
	profile.Kind = kind
	return &profile, nil
}

func (s *AuthStore) UpdateAccountProfile(ctx context.Context, accountSlug string, profile storage.AccountProfile) (*storage.AccountProfile, error) {
	if _, err := s.AccountKind(ctx, accountSlug); err != nil {
		return nil, err
	}
	s.b.mu.Lock()
	s.b.profiles[strings.TrimSpace(accountSlug)] = storage.AccountProfile{DisplayName: profile.DisplayName, Description: profile.Description, Website: profile.Website}
	s.b.mu.Unlock()
	return s.GetAccountProfile(ctx, accountSlug)
}
