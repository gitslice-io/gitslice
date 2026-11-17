package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *AuthService) CreateGroup(ctx context.Context, req *corev1.CreateGroupRequest) (*corev1.AccountGroup, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, name, err := resolveGroupInput(req.GetAccount(), req.GetName())
	if err != nil {
		return nil, err
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	description, err := normalizeSliceDescription(req.GetDescription())
	if err != nil {
		return nil, err
	}
	group, err := s.Auth.CreateGroup(ctx, account, name, description, subjectID)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return nil, status.Errorf(codes.AlreadyExists, "group %s already exists", storage.FormatGroupHandle(account, name))
		}
		return nil, grpcError(err)
	}
	return accountGroupProto(group), nil
}

func (s *AuthService) GetGroup(ctx context.Context, req *corev1.GetGroupRequest) (*corev1.AccountGroup, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, name, err := resolveGroupInput(req.GetAccount(), req.GetName())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeGroupRead(ctx, subjectID, account); err != nil {
		return nil, err
	}
	group, err := s.Auth.GetGroup(ctx, account, name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "group %s not found", storage.FormatGroupHandle(account, name))
		}
		return nil, grpcError(err)
	}
	return accountGroupProto(group), nil
}

func (s *AuthService) ListGroups(ctx context.Context, req *corev1.ListGroupsRequest) (*corev1.ListGroupsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, err := storage.NormalizeGroupAccount(strings.TrimPrefix(strings.TrimSpace(req.GetAccount()), "@"))
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.authorizeGroupRead(ctx, subjectID, account); err != nil {
		return nil, err
	}
	groups, err := s.Auth.ListGroups(ctx, account)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListGroupsResponse{
		Account: account,
		Groups:  make([]*corev1.AccountGroup, 0, len(groups)),
	}
	for i := range groups {
		out.Groups = append(out.Groups, accountGroupProto(&groups[i]))
	}
	return out, nil
}

func (s *AuthService) DeleteGroup(ctx context.Context, req *corev1.DeleteGroupRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, name, err := resolveGroupInput(req.GetAccount(), req.GetName())
	if err != nil {
		return nil, err
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	if err := s.Auth.DeleteGroup(ctx, account, name); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "group %s not found", storage.FormatGroupHandle(account, name))
		}
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *AuthService) AddGroupMember(ctx context.Context, req *corev1.AddGroupMemberRequest) (*corev1.AccountGroup, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, name, err := resolveGroupInput(req.GetAccount(), req.GetName())
	if err != nil {
		return nil, err
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	targetID, err := s.groupSubjectForUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	group, err := s.Auth.AddGroupMember(ctx, account, name, targetID, subjectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "group %s not found", storage.FormatGroupHandle(account, name))
		}
		return nil, grpcError(err)
	}
	return accountGroupProto(group), nil
}

func (s *AuthService) RemoveGroupMember(ctx context.Context, req *corev1.RemoveGroupMemberRequest) (*corev1.AccountGroup, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, name, err := resolveGroupInput(req.GetAccount(), req.GetName())
	if err != nil {
		return nil, err
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	targetID, err := s.groupSubjectForUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	group, err := s.Auth.RemoveGroupMember(ctx, account, name, targetID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "group %s or member %s not found", storage.FormatGroupHandle(account, name), strings.TrimSpace(req.GetUsername()))
		}
		return nil, grpcError(err)
	}
	return accountGroupProto(group), nil
}

func (s *AuthService) authorizeGroupRead(ctx context.Context, subjectID, account string) error {
	ok, err := canAuthorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionRead)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if s.Slices != nil {
		authorizer := authz.New(s.Auth)
		after := ""
		for {
			slices, err := s.Slices.List(ctx, account, after, 200, true)
			if err != nil {
				return grpcError(err)
			}
			for _, sl := range slices {
				if authorizer.AuthorizeAllowArchived(ctx, subjectID, sl, authz.ActionManageMembers) == nil {
					return nil
				}
			}
			if len(slices) < 200 {
				break
			}
			after = slices[len(slices)-1].GetRef().GetSlice()
		}
	}
	return status.Errorf(codes.PermissionDenied, "access to groups in account %q denied", account)
}

func (s *AuthService) groupSubjectForUsername(ctx context.Context, username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return "", status.Error(codes.InvalidArgument, "username is required")
	}
	subjectID, err := s.Auth.SubjectIDForUsername(ctx, username)
	if errors.Is(err, storage.ErrNotFound) {
		return "", status.Errorf(codes.NotFound, "no user or agent named %s", username)
	}
	if err != nil {
		return "", grpcError(err)
	}
	return subjectID, nil
}

func resolveGroupInput(rawAccount, rawName string) (string, string, error) {
	rawAccount = strings.TrimSpace(rawAccount)
	rawName = strings.TrimSpace(rawName)
	if strings.HasPrefix(rawName, "@") {
		parsedAcct, parsedName, err := storage.ParseGroupRefStrict(rawName)
		if err != nil {
			return "", "", grpcError(err)
		}
		if rawAccount != "" {
			acct, err := storage.NormalizeGroupAccount(rawAccount)
			if err != nil {
				return "", "", grpcError(err)
			}
			if acct != parsedAcct {
				return "", "", status.Errorf(codes.InvalidArgument, "account %q does not match group reference %s", acct, rawName)
			}
		}
		return parsedAcct, parsedName, nil
	}
	account, err := storage.NormalizeGroupAccount(rawAccount)
	if err != nil {
		return "", "", grpcError(err)
	}
	name, err := storage.NormalizeGroupName(rawName)
	if err != nil {
		return "", "", grpcError(err)
	}
	return account, name, nil
}

func accountGroupProto(g *storage.AccountGroup) *corev1.AccountGroup {
	if g == nil {
		return nil
	}
	out := &corev1.AccountGroup{
		Id:          g.ID,
		Account:     g.Account,
		Name:        g.Name,
		Handle:      g.Handle,
		Description: g.Description,
		CreatedBy:   g.CreatedBy,
		MemberCount: g.MemberCount,
		Members:     make([]*corev1.AccountGroupMember, 0, len(g.Members)),
	}
	if !g.CreatedAt.IsZero() {
		out.CreatedAt = g.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !g.UpdatedAt.IsZero() {
		out.UpdatedAt = g.UpdatedAt.UTC().Format(time.RFC3339)
	}
	for _, m := range g.Members {
		kind := "person"
		if m.Kind == "agent" {
			kind = "agent"
		}
		member := &corev1.AccountGroupMember{
			Username:    m.Username,
			SubjectId:   m.SubjectID,
			DisplayName: m.DisplayName,
			Kind:        kind,
			AddedBy:     m.AddedBy,
		}
		if !m.CreatedAt.IsZero() {
			member.CreatedAt = m.CreatedAt.UTC().Format(time.RFC3339)
		}
		out.Members = append(out.Members, member)
	}
	if out.MemberCount == 0 && len(out.Members) > 0 {
		out.MemberCount = int32(len(out.Members))
	}
	return out
}
