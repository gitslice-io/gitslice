package service

import (
	"context"

	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Services) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Ref == nil {
		return nil, status.Error(codes.InvalidArgument, "slice ref is required")
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, req.Ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Store.ResolveSlice(ctx, req.Ref)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *Services) GetSlice(ctx context.Context, req *corev1.GetSliceRequest) (*corev1.Slice, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	slice, err := s.Store.GetSlice(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *Services) ListSlices(ctx context.Context, req *corev1.ListSlicesRequest) (*corev1.ListSlicesResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, req.Account); err != nil {
		return nil, grpcError(err)
	}
	slices, err := s.Store.ListSlices(ctx, req.Account, int(req.PageSize))
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSlicesResponse{Slices: slices}, nil
}

func (s *Services) UpdateSliceDefinition(ctx context.Context, req *corev1.UpdateSliceDefinitionRequest) (*corev1.SliceDefinition, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	definition, err := s.Store.UpdateSliceDefinition(ctx, req.SliceId, req.ExpectedDefinitionHash, req.Definition)
	if err != nil {
		return nil, grpcError(err)
	}
	return definition, nil
}
