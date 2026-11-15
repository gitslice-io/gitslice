package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pathReads authorizes one subject's reads of paths: as a member of the
// path's account, or, signed in, through a slice that includes the path and
// that they can read (public, or shared with them;
// design/26_slice_members.md). Anonymous readers of a public slice read
// through the slice itself. It asks about each account once, however many
// paths it checks.
type pathReads struct {
	auth      storage.AuthStore
	slices    storage.SliceStore
	subjectID string
	accounts  map[string]error
}

func (r *pathReads) check(ctx context.Context, account, p string) error {
	if r.accounts == nil {
		r.accounts = map[string]error{}
	}
	err, seen := r.accounts[account]
	if !seen {
		err = authorizeAccount(ctx, r.auth, r.subjectID, account, authz.ActionRead)
		r.accounts[account] = err
	}
	if err == nil || strings.TrimSpace(r.subjectID) == "" {
		return err
	}
	ok, lookupErr := readableThroughSlice(ctx, r.auth, r.slices, r.subjectID, p)
	if lookupErr != nil {
		return lookupErr
	}
	if ok {
		return nil
	}
	return err
}

func (s *RepositoryService) ensureRepositoryPathRead(ctx context.Context, subjectID, p string) error {
	account := accountSlugForRepositoryPath(p)
	if account == "" {
		return nil
	}
	return ensurePathRead(ctx, s.Auth, s.Slices, subjectID, account, p)
}

// ensurePathRead lets the subject read path p in account (pathReads).
func ensurePathRead(ctx context.Context, auth storage.AuthStore, sliceStore storage.SliceStore, subjectID, account, p string) error {
	reads := pathReads{auth: auth, slices: sliceStore, subjectID: subjectID}
	return reads.check(ctx, account, p)
}

func (s *RepositoryService) readableAccountPrefixes(ctx context.Context, subjectID string) ([]string, error) {
	slugs, err := s.Auth.ListSubjectAccountSlugs(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	prefixes := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		prefixes = append(prefixes, "/"+slug)
	}
	return prefixes, nil
}

func (s *RepositoryService) ensureCommitRead(ctx context.Context, subjectID string, commit *corev1.Commit) error {
	if commit == nil || len(commit.ChangedPaths) == 0 {
		return nil
	}
	accounts := map[string][]string{}
	for _, changed := range commit.ChangedPaths {
		p, err := repositoryReadPath(changed)
		if err != nil {
			return status.Error(codes.Internal, "commit contains invalid changed path")
		}
		account := accountSlugForRepositoryPath(p)
		if account != "" {
			accounts[account] = append(accounts[account], p)
		}
	}
	if len(accounts) == 0 {
		return status.Error(codes.PermissionDenied, "commit is not readable")
	}
	// A commit is readable when each path it changes is: through the account,
	// or through a slice readable to the subject.
	for account, changed := range accounts {
		if authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionRead) == nil {
			continue
		}
		for _, p := range changed {
			if err := ensurePathRead(ctx, s.Auth, s.Slices, subjectID, account, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *RepositoryService) filterReadableRootEntries(ctx context.Context, subjectID string, entries []storage.TreeEntry) ([]storage.TreeEntry, error) {
	out := make([]storage.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind != "directory" || entry.Name == "" {
			continue
		}
		ok, err := canAuthorizeAccount(ctx, s.Auth, subjectID, entry.Name, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, entry)
		}
	}
	return out, nil
}

func accountSlugForRepositoryPath(p string) string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return ""
	}
	return strings.Split(trimmed, "/")[0]
}

func ensureRepositoryPathInSlice(slice *corev1.Slice, p string) error {
	if slice == nil || slice.Definition == nil {
		return status.Error(codes.InvalidArgument, "slice definition is required")
	}
	if paths.InAnyPrefix(slice.Definition.IncludedPaths, p) {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "path %s is outside slice %s/%s", p, slice.Ref.GetAccount(), slice.Ref.GetSlice())
}
