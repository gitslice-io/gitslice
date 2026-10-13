package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Slice members (design/26_slice_members.md): a person or agent given a role
// (admin, writer or reader) on one slice, without joining the account that
// owns it. The role adds to whatever the account gives them, within that
// slice only.

func (s *SliceService) ListSliceMembers(ctx context.Context, req *corev1.ListSliceMembersRequest) (*corev1.ListSliceMembersResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.resolveMemberSlice(ctx, req.GetSlice())
	if err != nil {
		return nil, err
	}
	// Anyone with a role here lists the members; a public slice's readers who
	// have none do not.
	if ok, err := hasSliceRole(ctx, s.Auth, subjectID, slice); err != nil {
		return nil, err
	} else if !ok {
		return nil, status.Error(codes.PermissionDenied, "only members of the slice or its account list its members")
	}
	members, err := s.Slices.ListMembers(ctx, slice.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListSliceMembersResponse{Slice: slice.Ref}
	for i := range members {
		out.Members = append(out.Members, sliceMemberProto(&members[i]))
	}
	return out, nil
}

func (s *SliceService) SetSliceMember(ctx context.Context, req *corev1.SetSliceMemberRequest) (*corev1.SliceMember, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(req.GetRole()))
	if !slices.Contains(storage.SliceRoles, role) {
		return nil, status.Errorf(codes.InvalidArgument, "role must be one of %s", strings.Join(storage.SliceRoles, ", "))
	}
	ref, err := normalizeServiceSliceRef(req.GetSlice())
	if err != nil {
		return nil, err
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionAdmin)
	if err != nil {
		return nil, err
	}
	memberID, err := s.subjectForUsername(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}
	addedBy, err := s.usernameOf(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	member, err := s.Slices.SetMember(ctx, slice.Id, memberID, role, addedBy)
	if err != nil {
		return nil, grpcError(err)
	}
	return sliceMemberProto(member), nil
}

func (s *SliceService) RemoveSliceMember(ctx context.Context, req *corev1.RemoveSliceMemberRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.resolveMemberSlice(ctx, req.GetSlice())
	if err != nil {
		return nil, err
	}
	memberID := subjectID
	if username := strings.TrimSpace(req.GetUsername()); username != "" {
		if memberID, err = s.subjectForUsername(ctx, username); err != nil {
			return nil, err
		}
	}
	// Members leave on their own; removing someone else takes the slice's
	// admins.
	if memberID != subjectID {
		if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionAdmin); err != nil {
			return nil, err
		}
	}
	if err := s.Slices.RemoveMember(ctx, slice.Id, memberID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "not a member of this slice")
		}
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *SliceService) ListSharedSlices(ctx context.Context, req *corev1.ListSharedSlicesRequest) (*corev1.ListSharedSlicesResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	shared, err := s.Slices.ListShared(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListSharedSlicesResponse{}
	for _, item := range shared {
		out.Slices = append(out.Slices, &corev1.SharedSlice{Slice: item.Slice, Role: item.Role})
	}
	return out, nil
}

// resolveMemberSlice finds a slice without authorizing; callers decide what
// the caller may do with its members.
func (s *SliceService) resolveMemberSlice(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	ref, err := normalizeServiceSliceRef(ref)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	// Someone who cannot read a private slice learns nothing of it here.
	if err := authorize(ctx, s.Auth, optionalSubject(ctx), slice, authz.ActionRead); err != nil {
		return nil, status.Error(codes.NotFound, "slice not found")
	}
	return slice, nil
}

func (s *SliceService) subjectForUsername(ctx context.Context, username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return "", status.Error(codes.InvalidArgument, "username is required")
	}
	subjectID, err := s.Auth.SubjectIDForUsername(ctx, username)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", status.Errorf(codes.NotFound, "no user or agent named %s", username)
		}
		return "", grpcError(err)
	}
	return subjectID, nil
}

func (s *SliceService) usernameOf(ctx context.Context, subjectID string) (string, error) {
	usernames, err := s.Auth.UsernamesForSubjects(ctx, []string{subjectID})
	if err != nil {
		return "", grpcError(err)
	}
	if name := usernames[subjectID]; name != "" {
		return name, nil
	}
	return subjectID, nil
}

// hasSliceRole reports whether the subject has any role on the slice: in its
// account, or on the slice itself.
func hasSliceRole(ctx context.Context, auth storage.AuthStore, subjectID string, slice *corev1.Slice) (bool, error) {
	ok, err := canAuthorizeAccount(ctx, auth, subjectID, slice.GetRef().GetAccount(), authz.ActionRead)
	if err != nil || ok {
		return ok, err
	}
	_, ok, err = authz.New(auth).SliceRole(ctx, subjectID, slice)
	if err != nil {
		return false, grpcError(err)
	}
	return ok, nil
}

// readableThroughSlice reports whether the subject may read path p because a
// slice that includes it is readable to them: public, or shared with them.
// Reads by path (not through a slice) start from the account; this lets a
// slice's readers outside the account read its files the same way.
func readableThroughSlice(ctx context.Context, auth storage.AuthStore, sliceStore storage.SliceStore, subjectID, p string) (bool, error) {
	if sliceStore == nil {
		return false, nil
	}
	covering, err := sliceStore.CoveringIDsByPath(ctx, []string{p})
	if err != nil {
		return false, grpcError(err)
	}
	for _, id := range covering[p] {
		slice, err := sliceStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return false, grpcError(err)
		}
		if authz.New(auth).Authorize(ctx, subjectID, slice, authz.ActionRead) == nil {
			return true, nil
		}
	}
	return false, nil
}

func sliceMemberProto(m *storage.SliceMember) *corev1.SliceMember {
	kind := "person"
	if m.Kind == "agent" {
		kind = "agent"
	}
	out := &corev1.SliceMember{
		Username:    m.Username,
		SubjectId:   m.SubjectID,
		Role:        m.Role,
		DisplayName: m.DisplayName,
		Kind:        kind,
		AddedBy:     m.AddedBy,
	}
	if !m.CreatedAt.IsZero() {
		out.CreatedAt = m.CreatedAt.UTC().Format(time.RFC3339)
	}
	return out
}
