package service

import (
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/authctx"
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

func (s *RepositoryService) readableScopedPrefixes(ctx context.Context, subjectID string, sliceIDs []string) ([]string, error) {
	if s.Slices == nil || len(sliceIDs) == 0 {
		return nil, nil
	}
	authorizer := authz.New(s.Auth)
	seen := make(map[string]struct{}, len(sliceIDs))
	var prefixes []string
	for _, sliceID := range sliceIDs {
		slice, err := s.Slices.Get(ctx, sliceID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return nil, grpcError(err)
		}
		if authorizer.Authorize(ctx, subjectID, slice, authz.ActionRead) != nil {
			continue
		}
		for _, raw := range slice.GetDefinition().GetIncludedPaths() {
			p, err := repositoryReadPath(raw)
			if err != nil || p == "" || p == "/" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			prefixes = append(prefixes, p)
		}
	}
	return prefixes, nil
}

func (s *RepositoryService) readableAccountPrefixes(ctx context.Context, subjectID string) ([]string, error) {
	if p, ok := authctx.PrincipalFrom(ctx); ok && p.KeyID != "" && p.SubjectID == subjectID && p.HasSliceScope {
		return s.readableScopedPrefixes(ctx, subjectID, p.SliceIDs)
	}
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
	if commit == nil {
		return status.Error(codes.NotFound, "commit not found")
	}
	if len(commit.ChangedPaths) == 0 {
		changesetID := strings.TrimSpace(commit.GetChangesetId())
		if changesetID == "" && s.Repository != nil && commit.GetId() != "" {
			if landed, err := s.Repository.ChangesetsForCommits(ctx, []string{commit.GetId()}); err == nil {
				changesetID = strings.TrimSpace(landed[commit.GetId()])
			}
		}
		if changesetID != "" && s.Changesets != nil {
			cs, err := s.Changesets.Get(ctx, changesetID)
			if err != nil {
				return grpcError(err)
			}
			if cs.GetAuthoringSlice() == nil {
				return status.Error(codes.PermissionDenied, "commit is not readable")
			}
			_, err = resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, cs.GetAuthoringSlice(), authz.ActionRead)
			return err
		}
		if strings.TrimSpace(subjectID) == "" {
			return status.Error(codes.PermissionDenied, "commit is not readable")
		}
		if p, ok := authctx.PrincipalFrom(ctx); ok && p.KeyID != "" && p.SubjectID == subjectID && p.HasSliceScope {
			return status.Error(codes.PermissionDenied, "commit is not readable")
		}
		prefixes, err := s.readableAccountPrefixes(ctx, subjectID)
		if err != nil {
			return err
		}
		if len(prefixes) == 0 {
			return status.Error(codes.PermissionDenied, "commit is not readable")
		}
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
	var allowedAccounts map[string]struct{}
	if p, ok := authctx.PrincipalFrom(ctx); ok && p.KeyID != "" && p.SubjectID == subjectID && p.HasSliceScope {
		prefixes, err := s.readableScopedPrefixes(ctx, subjectID, p.SliceIDs)
		if err != nil {
			return nil, err
		}
		allowedAccounts = make(map[string]struct{}, len(prefixes))
		for _, prefix := range prefixes {
			if slug := accountSlugForRepositoryPath(prefix); slug != "" {
				allowedAccounts[slug] = struct{}{}
			}
		}
	}
	out := make([]storage.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind != "directory" || entry.Name == "" {
			continue
		}
		if allowedAccounts != nil {
			if _, ok := allowedAccounts[entry.Name]; ok {
				out = append(out, entry)
			}
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

const maxPublishedRootTreeCache = 4096

func (s *RepositoryService) hasCachedPublishedRootTree(rootTreeID string) bool {
	s.publishedTreesMu.Lock()
	defer s.publishedTreesMu.Unlock()
	_, ok := s.publishedRootTrees[rootTreeID]
	return ok
}

func (s *RepositoryService) cachePublishedRootTree(rootTreeID string) {
	if rootTreeID == "" {
		return
	}
	s.publishedTreesMu.Lock()
	defer s.publishedTreesMu.Unlock()
	if s.publishedRootTrees == nil {
		s.publishedRootTrees = map[string]struct{}{}
	} else if len(s.publishedRootTrees) >= maxPublishedRootTreeCache {
		clear(s.publishedRootTrees)
	}
	s.publishedRootTrees[rootTreeID] = struct{}{}
}

// ensureAuthorizedRootTree verifies that a caller-supplied root_tree_id belongs
// either to a published commit on DefaultTargetRef or to a changeset whose
// authoring slice the caller is authorized to read.
func (s *RepositoryService) ensureAuthorizedRootTree(ctx context.Context, subjectID, rootTreeID string) error {
	rootTreeID = strings.TrimSpace(rootTreeID)
	if rootTreeID == "" {
		return nil
	}
	if s.hasCachedPublishedRootTree(rootTreeID) {
		return nil
	}
	if s.Repository != nil {
		if head, err := s.Repository.GetRef(ctx, storage.DefaultTargetRef); err == nil && head.GetCommitId() != "" {
			if headTree, err := s.Repository.RootTreeForCommit(ctx, head.GetCommitId()); err == nil && headTree != "" {
				s.cachePublishedRootTree(headTree)
				if headTree == rootTreeID {
					return nil
				}
			}
		}
		token := ""
		for {
			page, err := s.Repository.ListCommitPage(ctx, storage.DefaultTargetRef, 200, token)
			if err != nil {
				break
			}
			for _, commit := range page.Commits {
				if commit == nil {
					continue
				}
				treeID := strings.TrimSpace(commit.GetRootTreeId())
				if treeID == "" && commit.GetId() != "" {
					if loaded, err := s.Repository.RootTreeForCommit(ctx, commit.GetId()); err == nil {
						treeID = strings.TrimSpace(loaded)
					}
				}
				if treeID != "" {
					s.cachePublishedRootTree(treeID)
					if treeID == rootTreeID {
						return nil
					}
				}
			}
			if page.NextPageToken == "" {
				break
			}
			token = page.NextPageToken
		}
	}
	if s.Changesets != nil {
		matchedChangeset := false
		var firstAuthErr error
		token := ""
		for {
			list, nextToken, err := s.Changesets.List(ctx, storage.ChangesetListFilter{
				Limit:     200,
				PageToken: token,
				Summary:   false,
			})
			if err != nil {
				return grpcError(err)
			}
			for _, cs := range list {
				if cs == nil {
					continue
				}
				hasTree := false
				for _, ps := range cs.GetPatchsets() {
					if ps != nil && (strings.TrimSpace(ps.GetResultTreeId()) == rootTreeID || strings.TrimSpace(ps.GetBaseTreeId()) == rootTreeID) {
						hasTree = true
						break
					}
				}
				if !hasTree {
					continue
				}
				matchedChangeset = true
				if cs.GetAuthoringSlice() == nil {
					continue
				}
				if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, cs.GetAuthoringSlice(), authz.ActionRead); err == nil {
					return nil
				} else if firstAuthErr == nil {
					firstAuthErr = err
				}
			}
			if nextToken == "" {
				break
			}
			token = nextToken
		}
		if matchedChangeset {
			if firstAuthErr != nil {
				return firstAuthErr
			}
			return status.Error(codes.PermissionDenied, "root tree is not readable")
		}
	}
	return status.Error(codes.NotFound, "root tree not found")
}
