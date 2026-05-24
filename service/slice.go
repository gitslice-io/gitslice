package service

import (
	"context"
	"strings"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SliceService struct {
	Auth   *postgres.AuthStore
	Slices *postgres.SliceStore
}

func (s *SliceService) CreateSlice(ctx context.Context, req *corev1.CreateSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Ref)
	if err != nil {
		return nil, err
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Slices.Create(ctx, ref, req.IncludedPaths, req.Visibility)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *SliceService) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Ref)
	if err != nil {
		return nil, err
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *SliceService) GetSlice(ctx context.Context, req *corev1.GetSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, slice.Ref.Account); err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *SliceService) ListSlices(ctx context.Context, req *corev1.ListSlicesRequest) (*corev1.ListSlicesResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, err := normalizeServiceSlug(req.Account, "account")
	if err != nil {
		return nil, err
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, account); err != nil {
		return nil, grpcError(err)
	}
	slices, err := s.Slices.List(ctx, account, int(req.PageSize))
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSlicesResponse{Slices: slices}, nil
}

func (s *SliceService) UpdateSliceDefinition(ctx context.Context, req *corev1.UpdateSliceDefinitionRequest) (*corev1.SliceDefinition, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, current.Ref.Account); err != nil {
		return nil, grpcError(err)
	}
	definition, err := s.Slices.UpdateDefinition(ctx, req.SliceId, req.ExpectedDefinitionHash, req.Definition)
	if err != nil {
		return nil, grpcError(err)
	}
	return definition, nil
}

func (s *SliceService) DeleteSlice(ctx context.Context, req *corev1.DeleteSliceRequest) (*corev1.DeleteSliceResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, slice.Ref.Account); err != nil {
		return nil, grpcError(err)
	}
	if err := s.Slices.Delete(ctx, req.SliceId); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.DeleteSliceResponse{SliceId: req.SliceId}, nil
}

func normalizeServiceSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	if ref == nil {
		return nil, status.Error(codes.InvalidArgument, "slice ref is required")
	}
	account, err := normalizeServiceSlug(ref.Account, "account")
	if err != nil {
		return nil, err
	}
	slug, err := normalizeServiceSlug(ref.Slice, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

func normalizeServiceSlug(value, name string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", name)
	}
	if len(value) > 63 {
		return "", status.Errorf(codes.InvalidArgument, "%s must be 63 characters or fewer", name)
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", status.Errorf(codes.InvalidArgument, "%s must not start or end with '-'", name)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", status.Errorf(codes.InvalidArgument, "%s may contain only letters, numbers, '-' or '_'", name)
	}
	return value, nil
}
