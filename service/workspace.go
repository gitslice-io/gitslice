package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Services) GetWorkspaceState(ctx context.Context, req *corev1.GetWorkspaceStateRequest) (*corev1.WorkspaceState, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := sliceRefFromWorkspace(req.Workspace)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Store.ResolveSlice(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	target, err := s.Store.GetRef(ctx, postgres.DefaultTargetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.WorkspaceState{
		Ref:          req.Workspace,
		BaseCommitId: target.CommitId,
		Slice: &corev1.SliceBinding{
			Slice:               ref,
			SliceId:             slice.Id,
			SliceDefinitionHash: slice.DefinitionHash,
		},
		HydratedPaths: slice.Definition.IncludedPaths,
	}, nil
}

func (s *Services) HydratePaths(ctx context.Context, req *corev1.HydratePathsRequest) (*corev1.HydratePathsResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	if len(req.Paths) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one path is required")
	}
	target, err := s.Store.GetRef(ctx, postgres.DefaultTargetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	read, err := s.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: target.CommitId, Path: req.Paths[0]})
	if err != nil {
		return nil, err
	}
	resolved, err := s.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: target.CommitId, Path: req.Paths[0]})
	if err != nil {
		return nil, err
	}
	return &corev1.HydratePathsResponse{Path: req.Paths[0], Entry: resolved.Entry, Data: read.Data}, nil
}

func (s *Services) ValidateWorkspaceDiff(ctx context.Context, req *corev1.ValidateWorkspaceDiffRequest) (*corev1.ValidateWorkspaceDiffResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := sliceRefFromWorkspace(req.Workspace)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.Store.EnsureAccountMember(ctx, subjectID, ref.Account); err != nil {
		return nil, grpcError(err)
	}
	slice, err := s.Store.ResolveSlice(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	baseCommitID := req.BaseCommitId
	if baseCommitID == "" {
		target, err := s.Store.GetRef(ctx, postgres.DefaultTargetRef)
		if err != nil {
			return nil, grpcError(err)
		}
		baseCommitID = target.CommitId
	}
	validation, err := s.validateFileEdits(ctx, slice, baseCommitID, req.FileEdits, false)
	if err != nil {
		return nil, err
	}
	return validation, nil
}

func (s *Services) RecordWorkspaceOperation(ctx context.Context, req *corev1.RecordWorkspaceOperationRequest) (*corev1.RecordWorkspaceOperationResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	id, err := objectid.RandomID("op")
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.RecordWorkspaceOperationResponse{OperationId: id}, nil
}

func sliceRefFromWorkspace(ref *corev1.WorkspaceRef) (*corev1.SliceRef, error) {
	if ref == nil || ref.Id == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	parts := strings.Split(ref.Id, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("workspace id must be account/slice")
	}
	return &corev1.SliceRef{Account: parts[0], Slice: parts[1]}, nil
}
