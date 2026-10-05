package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Invitations to organizations and account profiles (design/12_account_auth.md
// 9.3). An invitation names a user and a role; it becomes a membership only
// when the user accepts it.

const (
	maxDisplayName = 64
	maxDescription = 280
	maxWebsite     = 200
)

func normalizeRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if storage.AccountRoleRank(role) >= len(storage.AccountRoles) {
		return "", status.Errorf(codes.InvalidArgument, "role must be one of %s", strings.Join(storage.AccountRoles, ", "))
	}
	return role, nil
}

func (s *AuthService) InviteAccountMember(ctx context.Context, req *corev1.InviteAccountMemberRequest) (*corev1.InviteAccountMemberResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	role, err := normalizeRole(req.GetRole())
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	if err := s.authorizeMemberChange(ctx, subjectID, account, "", role); err != nil {
		return nil, err
	}
	target, err := s.subjectForUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	if current, err := s.Auth.AccountRole(ctx, target, account); err == nil && current != "" {
		return nil, status.Errorf(codes.AlreadyExists, "%s is already a member of %s (%s); change their role instead", req.GetUsername(), account, current)
	}
	invitation, err := s.Auth.UpsertAccountInvitation(ctx, account, target, role, subjectID)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return nil, status.Error(codes.FailedPrecondition, "only organizations take invitations")
		}
		return nil, accountLookupError(err, account)
	}
	out, err := s.invitationsProto(ctx, []storage.AccountInvitation{*invitation})
	if err != nil {
		return nil, err
	}
	return &corev1.InviteAccountMemberResponse{Invitation: out[0]}, nil
}

func (s *AuthService) ListAccountInvitations(ctx context.Context, req *corev1.ListAccountInvitationsRequest) (*corev1.ListAccountInvitationsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	if err := s.authorizeMemberChange(ctx, subjectID, account, "", ""); err != nil {
		return nil, err
	}
	invitations, err := s.Auth.ListAccountInvitations(ctx, account)
	if err != nil {
		return nil, grpcError(err)
	}
	out, err := s.invitationsProto(ctx, invitations)
	if err != nil {
		return nil, err
	}
	return &corev1.ListAccountInvitationsResponse{Invitations: out}, nil
}

func (s *AuthService) ListMyInvitations(ctx context.Context, req *corev1.ListMyInvitationsRequest) (*corev1.ListMyInvitationsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	invitations, err := s.Auth.ListSubjectInvitations(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	out, err := s.invitationsProto(ctx, invitations)
	if err != nil {
		return nil, err
	}
	return &corev1.ListMyInvitationsResponse{Invitations: out}, nil
}

func (s *AuthService) RespondToInvitation(ctx context.Context, req *corev1.RespondToInvitationRequest) (*corev1.RespondToInvitationResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	if !req.GetAccept() {
		if err := s.Auth.DeleteAccountInvitation(ctx, account, subjectID); err != nil {
			return nil, invitationLookupError(err, account)
		}
		return &corev1.RespondToInvitationResponse{}, nil
	}
	role, err := s.Auth.AcceptAccountInvitation(ctx, account, subjectID)
	if err != nil {
		return nil, invitationLookupError(err, account)
	}
	return &corev1.RespondToInvitationResponse{Membership: &corev1.AccountMembership{
		Account: account,
		Kind:    storage.AccountKindOrganization,
		Role:    role,
	}}, nil
}

func (s *AuthService) CancelAccountInvitation(ctx context.Context, req *corev1.CancelAccountInvitationRequest) (*corev1.CancelAccountInvitationResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	if err := s.authorizeMemberChange(ctx, subjectID, account, "", ""); err != nil {
		return nil, err
	}
	target, err := s.subjectForUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	invitation, err := s.Auth.GetAccountInvitation(ctx, account, target)
	if err != nil {
		return nil, invitationLookupError(err, account)
	}
	// Only owners take back an invitation to become an owner.
	if err := s.authorizeMemberChange(ctx, subjectID, account, "", invitation.Role); err != nil {
		return nil, err
	}
	if err := s.Auth.DeleteAccountInvitation(ctx, account, target); err != nil {
		return nil, invitationLookupError(err, account)
	}
	return &corev1.CancelAccountInvitationResponse{}, nil
}

func (s *AuthService) GetAccountProfile(ctx context.Context, req *corev1.GetAccountProfileRequest) (*corev1.AccountProfile, error) {
	account := strings.TrimSpace(req.GetAccount())
	profile, err := s.Auth.GetAccountProfile(ctx, account)
	if err != nil {
		return nil, accountLookupError(err, account)
	}
	return accountProfileProto(profile), nil
}

func (s *AuthService) UpdateAccountProfile(ctx context.Context, req *corev1.UpdateAccountProfileRequest) (*corev1.AccountProfile, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	kind, err := s.Auth.AccountKind(ctx, account)
	if err != nil {
		return nil, accountLookupError(err, account)
	}
	if kind == storage.AccountKindOrganization {
		if err := s.authorizeMemberChange(ctx, subjectID, account, "", ""); err != nil {
			return nil, err
		}
	} else if !s.isOperator(subjectID) {
		names, err := s.Auth.UsernamesForSubjects(ctx, []string{subjectID})
		if err != nil {
			return nil, grpcError(err)
		}
		if names[subjectID] != account {
			return nil, status.Error(codes.PermissionDenied, "only its person can change a personal profile")
		}
	}
	profile := storage.AccountProfile{
		DisplayName: strings.TrimSpace(req.GetDisplayName()),
		Description: strings.TrimSpace(req.GetDescription()),
		Website:     strings.TrimSpace(req.GetWebsite()),
	}
	if utf8.RuneCountInString(profile.DisplayName) > maxDisplayName {
		return nil, status.Errorf(codes.InvalidArgument, "the display name is at most %d characters", maxDisplayName)
	}
	if utf8.RuneCountInString(profile.Description) > maxDescription {
		return nil, status.Errorf(codes.InvalidArgument, "the description is at most %d characters", maxDescription)
	}
	if profile.Website != "" {
		u, err := url.Parse(profile.Website)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(profile.Website) > maxWebsite {
			return nil, status.Errorf(codes.InvalidArgument, "the website must be an http or https URL of at most %d characters", maxWebsite)
		}
	}
	updated, err := s.Auth.UpdateAccountProfile(ctx, account, profile)
	if err != nil {
		return nil, accountLookupError(err, account)
	}
	return accountProfileProto(updated), nil
}

func (s *AuthService) invitationsProto(ctx context.Context, invitations []storage.AccountInvitation) ([]*corev1.AccountInvitation, error) {
	ids := make([]string, 0, 2*len(invitations))
	for _, invitation := range invitations {
		ids = append(ids, invitation.SubjectID, invitation.InvitedBySubjectID)
	}
	names, err := s.Auth.UsernamesForSubjects(ctx, ids)
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*corev1.AccountInvitation, 0, len(invitations))
	for _, invitation := range invitations {
		out = append(out, &corev1.AccountInvitation{
			Account:   invitation.Account,
			Username:  names[invitation.SubjectID],
			Role:      invitation.Role,
			InvitedBy: names[invitation.InvitedBySubjectID],
			CreatedAt: formatTime(invitation.CreatedAt),
		})
	}
	return out, nil
}

func accountProfileProto(profile *storage.AccountProfile) *corev1.AccountProfile {
	return &corev1.AccountProfile{
		Account:     profile.Account,
		Kind:        profile.Kind,
		DisplayName: profile.DisplayName,
		Description: profile.Description,
		Website:     profile.Website,
		CreatedAt:   formatTime(profile.CreatedAt),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func invitationLookupError(err error, account string) error {
	if errors.Is(err, storage.ErrNotFound) {
		return status.Errorf(codes.NotFound, "no pending invitation to %s", account)
	}
	return grpcError(err)
}
