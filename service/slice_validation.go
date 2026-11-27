package service

import (
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

func (s *SliceService) validateSliceApprovers(ctx context.Context, subjectID, sliceAccount, approvers string) error {
	approvers = strings.TrimSpace(approvers)
	if approvers == "" {
		return nil
	}
	groupAccount, groupName, err := storage.ParseGroupRefStrict(approvers)
	if err != nil {
		return grpcError(err)
	}
	if groupAccount != sliceAccount {
		canRead, err := canAuthorizeAccount(ctx, s.Auth, subjectID, groupAccount, authz.ActionRead)
		if err != nil {
			return err
		}
		if !canRead {
			return status.Errorf(codes.NotFound, "no group named %s", storage.FormatGroupHandle(groupAccount, groupName))
		}
	}
	if _, err := s.Auth.GetGroup(ctx, groupAccount, groupName); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return status.Errorf(codes.NotFound, "no group named %s", storage.FormatGroupHandle(groupAccount, groupName))
		}
		return grpcError(err)
	}
	return nil
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
