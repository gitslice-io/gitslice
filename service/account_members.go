package service

import (
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/usernames"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Organization accounts (design/12_account_auth.md). Any user with a personal
// account may create organizations, up to maxCreatedOrganizations; operators,
// listed in the server's GITSLICE_OPERATOR_SUBJECTS, may also use names
// reserved for self-service sign-up and name other owners. Owners and admins
// manage members after that, inviting new ones.

// maxCreatedOrganizations caps how many organizations one user may create.
const maxCreatedOrganizations = 20

func (s *AuthService) isOperator(subjectID string) bool {
	for _, operator := range s.OperatorSubjects {
		if operator != "" && operator == subjectID {
			return true
		}
	}
	return false
}

func (s *AuthService) CreateOrganization(ctx context.Context, req *corev1.CreateOrganizationRequest) (*corev1.CreateOrganizationResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	operator := s.isOperator(subjectID)
	normalize := usernames.Normalize
	if operator {
		normalize = usernames.NormalizeSyntax
	}
	slug, err := normalize(req.GetSlug())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid organization name: %v", err)
	}
	if !operator {
		// A user creates an organization for themselves: they are its only owner
		// and invite the rest.
		names, err := s.Auth.UsernamesForSubjects(ctx, []string{subjectID})
		if err != nil {
			return nil, grpcError(err)
		}
		self := names[subjectID]
		if self == "" {
			return nil, status.Error(codes.FailedPrecondition, "choose a username before creating an organization")
		}
		for _, owner := range req.GetOwnerUsernames() {
			if !strings.EqualFold(strings.TrimSpace(owner), self) {
				return nil, status.Error(codes.InvalidArgument, "you are the first owner; invite other owners after creating it")
			}
		}
		created, err := s.Auth.CountOrganizationsCreatedBy(ctx, subjectID)
		if err != nil {
			return nil, grpcError(err)
		}
		if created >= maxCreatedOrganizations {
			return nil, status.Errorf(codes.ResourceExhausted, "you have created %d organizations, the most allowed", created)
		}
		req = &corev1.CreateOrganizationRequest{Slug: slug}
	}
	if _, err := s.Auth.AccountKind(ctx, slug); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "account %q already exists", slug)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, grpcError(err)
	}
	owners := []string{subjectID}
	if len(req.GetOwnerUsernames()) > 0 {
		owners = owners[:0]
		for _, username := range req.GetOwnerUsernames() {
			owner, err := s.subjectForUsername(ctx, username)
			if err != nil {
				return nil, err
			}
			owners = append(owners, owner)
		}
	}
	if err := s.Auth.CreateOrganization(ctx, slug, owners, subjectID); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return nil, status.Errorf(codes.AlreadyExists, "account %q already exists", slug)
		}
		return nil, grpcError(err)
	}
	members, err := s.Auth.ListAccountMembers(ctx, slug)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.CreateOrganizationResponse{Account: slug, Members: accountMembersProto(members)}, nil
}

func (s *AuthService) ListAccountMembers(ctx context.Context, req *corev1.ListAccountMembersRequest) (*corev1.ListAccountMembersResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	kind, err := s.Auth.AccountKind(ctx, account)
	if err != nil {
		return nil, accountLookupError(err, account)
	}
	if !s.isOperator(subjectID) {
		if _, err := s.Auth.AccountRole(ctx, subjectID, account); err != nil {
			// Non-members cannot tell whether the account exists.
			return nil, accountLookupError(storage.ErrNotFound, account)
		}
	}
	members, err := s.Auth.ListAccountMembers(ctx, account)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListAccountMembersResponse{Account: account, Kind: kind, Members: accountMembersProto(members)}, nil
}

func (s *AuthService) SetAccountMember(ctx context.Context, req *corev1.SetAccountMemberRequest) (*corev1.SetAccountMemberResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(req.GetRole()))
	if storage.AccountRoleRank(role) >= len(storage.AccountRoles) {
		return nil, status.Errorf(codes.InvalidArgument, "role must be one of %s", strings.Join(storage.AccountRoles, ", "))
	}
	account := strings.TrimSpace(req.GetAccount())
	target, err := s.subjectForUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	current, _ := s.Auth.AccountRole(ctx, target, account)
	if err := s.authorizeMemberChange(ctx, subjectID, account, current, role); err != nil {
		return nil, err
	}
	// New people are invited and join by accepting; only operators add them
	// directly (server automation, such as ops/selfhost/phase1.sh).
	if current == "" && !s.isOperator(subjectID) {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is not a member of %s; invite them instead (gs account invite %s %s)", req.GetUsername(), account, account, req.GetUsername())
	}
	if err := s.Auth.SetAccountMemberRole(ctx, account, target, role); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.SetAccountMemberResponse{Member: &corev1.AccountMember{
		Username:  strings.TrimSpace(req.GetUsername()),
		SubjectId: target,
		Role:      role,
	}}, nil
}

func (s *AuthService) RemoveAccountMember(ctx context.Context, req *corev1.RemoveAccountMemberRequest) (*corev1.RemoveAccountMemberResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	target, err := s.subjectForUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	current, err := s.Auth.AccountRole(ctx, target, account)
	if err != nil {
		if err := s.authorizeMemberChange(ctx, subjectID, account, "", ""); err != nil {
			return nil, err
		}
		return nil, status.Errorf(codes.NotFound, "%s is not a member of %s", req.GetUsername(), account)
	}
	if err := s.authorizeMemberChange(ctx, subjectID, account, current, ""); err != nil {
		return nil, err
	}
	if err := s.Auth.RemoveAccountMember(ctx, account, target); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.RemoveAccountMemberResponse{}, nil
}

// authorizeMemberChange allows operators, owners, and admins to change
// memberships; granting, revoking or modifying the owner role needs an owner
// (or operator).
func (s *AuthService) authorizeMemberChange(ctx context.Context, subjectID, account, currentRole, newRole string) error {
	if s.isOperator(subjectID) {
		if _, err := s.Auth.AccountKind(ctx, account); err != nil {
			return accountLookupError(err, account)
		}
		return nil
	}
	callerRole, err := s.Auth.AccountRole(ctx, subjectID, account)
	if err != nil {
		return accountLookupError(storage.ErrNotFound, account)
	}
	switch callerRole {
	case "owner":
		return nil
	case "admin":
		if currentRole == "owner" || newRole == "owner" {
			return status.Error(codes.PermissionDenied, "only owners can grant or revoke the owner role")
		}
		return nil
	default:
		return status.Errorf(codes.PermissionDenied, "managing members of %s requires the owner or admin role", account)
	}
}

func (s *AuthService) subjectForUsername(ctx context.Context, username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", status.Error(codes.InvalidArgument, "username is required")
	}
	subjectID, err := s.Auth.SubjectIDForUsername(ctx, username)
	if errors.Is(err, storage.ErrNotFound) {
		return "", status.Errorf(codes.InvalidArgument, "no user named %q", username)
	}
	if err != nil {
		return "", grpcError(err)
	}
	return subjectID, nil
}

func accountLookupError(err error, account string) error {
	if errors.Is(err, storage.ErrNotFound) {
		return status.Errorf(codes.NotFound, "account %q not found", account)
	}
	return grpcError(err)
}

func accountMembersProto(members []storage.AccountMember) []*corev1.AccountMember {
	out := make([]*corev1.AccountMember, 0, len(members))
	for _, member := range members {
		out = append(out, &corev1.AccountMember{Username: member.Username, SubjectId: member.SubjectID, Role: member.Role})
	}
	return out
}
