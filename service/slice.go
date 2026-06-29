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
	includedPaths, visibility, requiredApprovals, requiredChecks, err := s.Slices.ValidateDefinition(ref, req.IncludedPaths, req.Visibility, req.RequiredApprovals, req.RequiredChecks)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.validateIncludedPathsExist(ctx, ref, includedPaths); err != nil {
		return nil, err
	}
	slice, err := s.Slices.Create(ctx, subjectID, ref, includedPaths, visibility, requiredApprovals, requiredChecks)
	if err != nil {
		return nil, grpcError(err)
	}
	return slice, nil
}

func (s *SliceService) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	subjectID := optionalSubject(ctx)
	ref, err := normalizeServiceSliceRef(req.Ref)
	if err != nil {
		return nil, err
	}
	return resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionRead)
}

func (s *SliceService) GetSlice(ctx context.Context, req *corev1.GetSliceRequest) (*corev1.Slice, error) {
	subjectID := optionalSubject(ctx)
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

func (s *SliceService) ListSliceDefinitionVersions(ctx context.Context, req *corev1.ListSliceDefinitionVersionsRequest) (*corev1.ListSliceDefinitionVersionsResponse, error) {
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
	versions, err := s.Slices.ListDefinitionVersions(ctx, req.SliceId, int(req.PageSize))
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSliceDefinitionVersionsResponse{Versions: versions}, nil
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
	includedPaths, visibility, requiredApprovals, requiredChecks, err := s.Slices.ValidateDefinition(current.Ref, req.Definition.IncludedPaths, req.Definition.Visibility, req.Definition.RequiredApprovals, req.Definition.RequiredChecks)
	if err != nil {
		return nil, grpcError(err)
	}
	// The home slice's included paths are managed by account provisioning and
	// must not be edited; other definition fields (e.g. visibility) may change.
	if isHomeSlice(current.Ref) &&
		!sameStringSet(current.Definition.GetIncludedPaths(), includedPaths) {
		return nil, status.Error(codes.InvalidArgument, "the home slice's included paths cannot be changed")
	}
	if err := s.validateIncludedPathsExist(ctx, current.Ref, addedIncludedPaths(current.Definition, includedPaths)); err != nil {
		return nil, err
	}
	definition, err := s.Slices.UpdateDefinition(ctx, subjectID, req.SliceId, req.ExpectedDefinitionHash, &corev1.SliceDefinition{
		IncludedPaths:     includedPaths,
		Visibility:        visibility,
		RequiredApprovals: requiredApprovals,
		RequiredChecks:    requiredChecks,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return definition, nil
}

func (s *SliceService) SetSliceCIDaemon(ctx context.Context, req *corev1.SetSliceCIDaemonRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, current, authz.ActionAdmin); err != nil {
		return nil, err
	}
	updated, err := s.Slices.SetCIDaemon(ctx, current.Id, req.DaemonId)
	if err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

func (s *SliceService) SetSliceSecret(ctx context.Context, req *corev1.SetSliceSecretRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, current, authz.ActionAdmin); err != nil {
		return nil, err
	}
	if err := s.Slices.SetSliceSecret(ctx, current.Id, req.Name, req.Value); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *SliceService) DeleteSliceSecret(ctx context.Context, req *corev1.DeleteSliceSecretRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, current, authz.ActionAdmin); err != nil {
		return nil, err
	}
	if err := s.Slices.DeleteSliceSecret(ctx, current.Id, req.Name); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *SliceService) ListSliceSecrets(ctx context.Context, req *corev1.ListSliceSecretsRequest) (*corev1.ListSliceSecretsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, current, authz.ActionWrite); err != nil {
		return nil, err
	}
	names, err := s.Slices.ListSliceSecretNames(ctx, current.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSliceSecretsResponse{Names: names}, nil
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

func isHomeSlice(ref *corev1.SliceRef) bool {
	return ref != nil && ref.Slice == "home"
}

// sameStringSet reports whether a and b contain the same elements regardless of
// order or duplication count differences beyond membership multiplicity.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	return true
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
