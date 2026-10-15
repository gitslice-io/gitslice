package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListDirectory lists direct children at a repository path.
//
// Without req.Slice, this is a normal global-tree directory read backed by the
// immutable tree stored for the requested commit. With req.Slice, the response
// is a slice projection: callers still ask for canonical repository paths, but
// the service hides files and directories outside the requested slice's
// included paths. The projected form is what the web app and shell-like
// clients use to browse a custom slice without seeing unrelated repository
// folders.
func (s *RepositoryService) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	subjectID := optionalSubject(ctx)
	if req.Slice != nil {
		return s.listSliceDirectory(ctx, subjectID, req)
	}
	p := req.Path
	var err error
	p, err = repositoryReadPath(p)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
		return nil, err
	}
	// The root mixes accounts with different readers, so it is listed a level
	// at a time.
	if req.Recursive && accountSlugForRepositoryPath(p) != "" {
		return s.listTree(ctx, req, p)
	}
	entries, err := repositoryListDirectoryAtSource(ctx, s.Repository, req.CommitId, req.RootTreeId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	if p == "/" {
		entries, err = s.filterReadableRootEntries(ctx, subjectID, entries)
		if err != nil {
			return nil, err
		}
	}
	out := make([]*corev1.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, treeEntryFromRepositoryEntry(entry))
	}
	page, nextCursor := paginateDirectoryEntries(out, req.PageSize, req.Cursor)
	return &corev1.ListDirectoryResponse{Entries: page, NextCursor: nextCursor}, nil
}

// listSliceDirectory implements ListDirectory's optional slice projection.
//
// Authorization is checked against the slice account before resolving the slice
// definition. The requested path is normalized with repositoryReadPath so
// ancestor directories such as "/" and "/acme" remain browsable; the slice
// definition's included paths are validated separately. A request inside an
// included prefix exposes the real directory at that path. A request above one
// or more included prefixes exposes only the immediate child on the way to each
// included prefix without walking the projected descendants.
func (s *RepositoryService) listSliceDirectory(ctx context.Context, subjectID string, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	if req.Slice.Account == "" || req.Slice.Slice == "" {
		return nil, status.Error(codes.InvalidArgument, "slice ref is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
	if err != nil {
		return nil, err
	}
	p, err := repositoryReadPath(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.Recursive && sliceIncludesPath(slice, p) {
		return s.listTree(ctx, req, p)
	}
	if direct, ok, err := s.sliceContainedDirectoryEntries(ctx, slice, req.CommitId, req.RootTreeId, p); ok || err != nil {
		if err != nil {
			return nil, err
		}
		page, nextCursor := paginateDirectoryEntries(direct, req.PageSize, req.Cursor)
		return &corev1.ListDirectoryResponse{Entries: page, NextCursor: nextCursor}, nil
	}
	projected, err := s.sliceProjectedDirectoryEntriesShallow(ctx, slice, req.CommitId, req.RootTreeId, p)
	if err != nil {
		return nil, err
	}
	page, nextCursor := paginateDirectoryEntries(projected, req.PageSize, req.Cursor)
	return &corev1.ListDirectoryResponse{Entries: page, NextCursor: nextCursor}, nil
}

// sliceIncludesPath reports whether p is inside one of the slice's included
// paths, so everything under it is in the slice.
func sliceIncludesPath(slice *corev1.Slice, p string) bool {
	for _, prefix := range slice.GetDefinition().GetIncludedPaths() {
		if canonical, err := repositoryReadPath(prefix); err == nil && repositoryPathContains(canonical, p) {
			return true
		}
	}
	return false
}

const listTreeMaxPage = 10000

// listTree answers a recursive ListDirectory: everything under p, paged by
// path (the cursor is the last path of the previous page), up to
// listTreeMaxPage entries a page. A checkout lists its slice in one or a few
// calls instead of one per directory (design/28_benchmarks.md).
func (s *RepositoryService) listTree(ctx context.Context, req *corev1.ListDirectoryRequest, p string) (*corev1.ListDirectoryResponse, error) {
	rootTreeID := strings.TrimSpace(req.GetRootTreeId())
	if rootTreeID == "" {
		var err error
		if rootTreeID, err = s.Repository.RootTreeForCommit(ctx, req.GetCommitId()); err != nil {
			return nil, grpcError(err)
		}
	}
	entries, err := s.Repository.ListTreeAtTree(ctx, rootTreeID, p)
	if err != nil {
		return nil, grpcError(err)
	}
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 || pageSize > listTreeMaxPage {
		pageSize = listTreeMaxPage
	}
	start := 0
	if cursor := strings.TrimSpace(req.GetCursor()); cursor != "" {
		start = sort.Search(len(entries), func(i int) bool { return entries[i].Path > cursor })
	}
	end := min(start+pageSize, len(entries))
	out := make([]*corev1.TreeEntry, 0, end-start)
	for _, entry := range entries[start:end] {
		out = append(out, treeEntryFromRepositoryEntry(entry))
	}
	next := ""
	if end < len(entries) {
		next = entries[end-1].Path
	}
	return &corev1.ListDirectoryResponse{Entries: out, NextCursor: next, Recursive: true}, nil
}

func (s *RepositoryService) sliceContainedDirectoryEntries(ctx context.Context, slice *corev1.Slice, commitID, rootTreeID, p string) ([]*corev1.TreeEntry, bool, error) {
	byPath := map[string]*corev1.TreeEntry{}
	matched := false
	for _, prefix := range slice.Definition.IncludedPaths {
		canonical, err := repositoryReadPath(prefix)
		if err != nil {
			return nil, false, status.Error(codes.InvalidArgument, err.Error())
		}
		if !repositoryPathContains(canonical, p) {
			continue
		}
		matched = true
		entries, err := repositoryListDirectoryAtSource(ctx, s.Repository, commitID, rootTreeID, p)
		if err != nil {
			return nil, true, grpcError(err)
		}
		for _, entry := range entries {
			byPath[entry.Path] = treeEntryFromRepositoryEntry(entry)
		}
	}
	if !matched {
		return nil, false, nil
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out, true, nil
}

func (s *RepositoryService) sliceProjectedDirectoryEntriesShallow(ctx context.Context, slice *corev1.Slice, commitID, rootTreeID, p string) ([]*corev1.TreeEntry, error) {
	byPath := map[string]*corev1.TreeEntry{}
	for _, prefix := range slice.Definition.IncludedPaths {
		canonical, err := repositoryReadPath(prefix)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		switch {
		case repositoryPathContains(canonical, p):
			entries, err := repositoryListDirectoryAtSource(ctx, s.Repository, commitID, rootTreeID, p)
			if err != nil {
				return nil, grpcError(err)
			}
			for _, entry := range entries {
				byPath[entry.Path] = treeEntryFromRepositoryEntry(entry)
			}
		case repositoryPathContains(p, canonical):
			childPath, ok := immediateChildPath(p, canonical)
			if !ok {
				continue
			}
			if _, ok := byPath[childPath]; ok {
				continue
			}
			entry, err := repositoryGetEntryAtSource(ctx, s.Repository, commitID, rootTreeID, childPath)
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, grpcError(err)
			}
			byPath[childPath] = treeEntryFromRepositoryEntry(*entry)
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out, nil
}

// sliceProjectedEntries returns the tree entries visible through a slice at a
// commit.
//
// Each included path is normalized with repositoryReadPath so home slices can
// project an account root such as /nic. Entries can appear under more than one
// included path if definitions overlap, so results are keyed by full repository
// path before sorting. Walking tree entries rather than only files preserves
// empty directories in custom-slice projections.
func (s *RepositoryService) sliceProjectedEntries(ctx context.Context, slice *corev1.Slice, commitID string) ([]storage.TreeEntry, error) {
	byPath := map[string]storage.TreeEntry{}
	for _, prefix := range slice.Definition.IncludedPaths {
		canonical, err := repositoryReadPath(prefix)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if err := s.collectProjectedEntries(ctx, commitID, canonical, byPath); err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]storage.TreeEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out, nil
}

func (s *RepositoryService) collectProjectedEntries(ctx context.Context, commitID, p string, byPath map[string]storage.TreeEntry) error {
	entry, err := s.Repository.GetEntry(ctx, commitID, p)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return grpcError(err)
	}
	byPath[entry.Path] = *entry
	if entry.Kind != "directory" {
		return nil
	}
	children, err := s.Repository.ListDirectory(ctx, commitID, entry.Path)
	if err != nil {
		return grpcError(err)
	}
	for _, child := range children {
		byPath[child.Path] = child
		if child.Kind != "directory" {
			continue
		}
		if err := s.collectProjectedEntries(ctx, commitID, child.Path, byPath); err != nil {
			return err
		}
	}
	return nil
}
