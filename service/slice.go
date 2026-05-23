package service

import (
	"context"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SliceService struct {
	Auth   *postgres.AuthStore
	Slices *postgres.SliceStore
}

func (s *SliceService) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Ref == nil {
		return nil, status.Error(codes.InvalidArgument, "slice ref is required")
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, req.Ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Slices.Resolve(ctx, req.Ref)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *SliceService) GetSlice(ctx context.Context, req *corev1.GetSliceRequest) (*corev1.Slice, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	slice, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *SliceService) ListSlices(ctx context.Context, req *corev1.ListSlicesRequest) (*corev1.ListSlicesResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Auth.EnsureAccountMember(ctx, subjectID, req.Account); err != nil {
		return nil, grpcError(err)
	}
	slices, err := s.Slices.List(ctx, req.Account, int(req.PageSize))
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSlicesResponse{Slices: slices}, nil
}

func (s *SliceService) UpdateSliceDefinition(ctx context.Context, req *corev1.UpdateSliceDefinitionRequest) (*corev1.SliceDefinition, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	definition, err := s.Slices.UpdateDefinition(ctx, req.SliceId, req.ExpectedDefinitionHash, req.Definition)
	if err != nil {
		return nil, grpcError(err)
	}
	return definition, nil
}
