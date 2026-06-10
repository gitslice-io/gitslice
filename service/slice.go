package service

import (
	"context"
	"errors"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SliceService struct {
	Auth       storage.AuthStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
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
	if err := authorizeAccount(ctx, s.Auth, subjectID, ref.Account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	includedPaths, visibility, err := s.Slices.ValidateDefinition(ref, req.IncludedPaths, req.Visibility)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.validateIncludedPathsExist(ctx, ref, includedPaths); err != nil {
		return nil, err
	}
	slice, err := s.Slices.Create(ctx, ref, includedPaths, visibility)
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
	return resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionRead)
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
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionRead); err != nil {
		return nil, err
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
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionRead); err != nil {
		return nil, err
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
	if err := authorize(ctx, s.Auth, subjectID, current, authz.ActionAdmin); err != nil {
		return nil, err
	}
	if req.Definition == nil {
		return nil, status.Error(codes.InvalidArgument, "slice definition is required")
	}
	includedPaths, visibility, err := s.Slices.ValidateDefinition(current.Ref, req.Definition.IncludedPaths, req.Definition.Visibility)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.validateIncludedPathsExist(ctx, current.Ref, addedIncludedPaths(current.Definition, includedPaths)); err != nil {
		return nil, err
	}
	definition, err := s.Slices.UpdateDefinition(ctx, req.SliceId, req.ExpectedDefinitionHash, &corev1.SliceDefinition{
		IncludedPaths: includedPaths,
		Visibility:    visibility,
	})
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
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionAdmin); err != nil {
		return nil, err
	}
	if err := s.Slices.Delete(ctx, req.SliceId); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.DeleteSliceResponse{SliceId: req.SliceId}, nil
}

// addedIncludedPaths returns the requested included paths that are not already
// part of the current definition, so updates that keep existing paths (for
// example visibility-only changes) do not re-require committed content.
func addedIncludedPaths(current *corev1.SliceDefinition, requested []string) []string {
	existing := map[string]struct{}{}
	if current != nil {
		for _, p := range current.IncludedPaths {
			existing[p] = struct{}{}
		}
	}
	added := make([]string, 0, len(requested))
	for _, p := range requested {
		if _, ok := existing[p]; !ok {
			added = append(added, p)
		}
	}
	return added
}

func (s *SliceService) validateIncludedPathsExist(ctx context.Context, ref *corev1.SliceRef, includedPaths []string) error {
	if s.Repository == nil {
		return status.Error(codes.Internal, "repository store is not configured")
	}
	target, err := s.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		return grpcError(err)
	}
	for _, p := range includedPaths {
		if ref != nil && ref.Slice == "home" && p == "/"+ref.Account {
			continue
		}
		if _, err := s.Repository.GetEntry(ctx, target.CommitId, p); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return status.Errorf(codes.FailedPrecondition, "included path does not exist: %s", p)
			}
			return grpcError(err)
		}
	}
	return nil
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
