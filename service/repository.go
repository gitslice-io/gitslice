package service

import (
	"context"
	"errors"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RepositoryService struct {
	Repository  *postgres.RepositoryStore
	ObjectStore ObjectStore
}

func (s *RepositoryService) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	return resolvePath(ctx, s.Repository, req)
}

func resolvePath(ctx context.Context, repository *postgres.RepositoryStore, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	p, err := paths.Canonical(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entry, err := repository.GetFile(ctx, req.CommitId, p)
	if err == nil {
		return &corev1.ResolvePathResponse{Entry: treeEntryFromFile(*entry)}, nil
	}
	if !errors.Is(err, postgres.ErrNotFound) {
		return nil, grpcError(err)
	}
	children, err := repository.ListFiles(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	if len(children) == 0 {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	return &corev1.ResolvePathResponse{Entry: &corev1.TreeEntry{
		Path:   p,
		Name:   path.Base(p),
		Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		TreeId: directoryTreeID(p, children),
	}}, nil
}

func (s *RepositoryService) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	p := req.Path
	if p == "" {
		p = "/"
	}
	if p != "/" {
		var err error
		p, err = paths.Canonical(p)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	files, err := s.Repository.ListFiles(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	entries := immediateDirectoryEntries(p, files)
	return &corev1.ListDirectoryResponse{Entries: entries}, nil
}

func (s *RepositoryService) ReadFile(ctx context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	return readFile(ctx, s.Repository, s.ObjectStore, req)
}

func readFile(ctx context.Context, repository *postgres.RepositoryStore, objectStore ObjectStore, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	p, err := paths.Canonical(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entry, err := repository.GetFile(ctx, req.CommitId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	rc, err := objectStore.Get(ctx, filesystem.BlobKey(entry.ContentHash), req.Offset, req.Length)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ReadFileResponse{Data: data, Offset: req.Offset, ContentHash: entry.ContentHash}, nil
}

func (s *RepositoryService) GetCommit(ctx context.Context, req *corev1.GetCommitRequest) (*corev1.Commit, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	commit, err := s.Repository.GetCommit(ctx, req.CommitId)
	if err != nil {
		return nil, grpcError(err)
	}
	return commit, nil
}

func (s *RepositoryService) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	refName := req.RefName
	if refName == "" {
		refName = postgres.DefaultTargetRef
	}
	ref, err := s.Repository.GetRef(ctx, refName)
	if err != nil {
		return nil, grpcError(err)
	}
	return ref, nil
}

func treeEntryFromFile(entry postgres.FileEntry) *corev1.TreeEntry {
	return &corev1.TreeEntry{
		Path:        entry.Path,
		Name:        path.Base(entry.Path),
		Kind:        corev1.EntryKind_ENTRY_KIND_FILE,
		Mode:        entry.Mode,
		BlobId:      entry.BlobID,
		Size:        entry.Size,
		ContentHash: entry.ContentHash,
	}
}

func immediateDirectoryEntries(prefix string, files []postgres.FileEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, file := range files {
		rel := strings.TrimPrefix(file.Path, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			byPath[file.Path] = treeEntryFromFile(file)
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[childPath] = treeEntryFromFile(file)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{
				Path:   childPath,
				Name:   parts[0],
				Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
				TreeId: directoryTreeID(childPath, files),
			}
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
	return out
}

func directoryTreeID(prefix string, files []postgres.FileEntry) string {
	entries := immediateDirectoryEntriesWithoutIDs(prefix, files)
	return objectid.TreeID(entries)
}

func immediateDirectoryEntriesWithoutIDs(prefix string, files []postgres.FileEntry) []objectid.TreeEntry {
	uiEntries := immediateDirectoryEntriesNoTree(prefix, files)
	entries := make([]objectid.TreeEntry, 0, len(uiEntries))
	for _, entry := range uiEntries {
		entries = append(entries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        entry.Kind.String(),
			Mode:        entry.Mode,
			BlobID:      entry.BlobId,
			Size:        entry.Size,
			ContentHash: entry.ContentHash,
		})
	}
	return entries
}

func immediateDirectoryEntriesNoTree(prefix string, files []postgres.FileEntry) []*corev1.TreeEntry {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	byPath := map[string]*corev1.TreeEntry{}
	for _, file := range files {
		rel := strings.TrimPrefix(file.Path, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			byPath[file.Path] = treeEntryFromFile(file)
			continue
		}
		parts := strings.Split(rel, "/")
		childPath := strings.TrimRight(prefix, "/") + "/" + parts[0]
		if prefix == "/" {
			childPath = "/" + parts[0]
		}
		if len(parts) == 1 {
			byPath[childPath] = treeEntryFromFile(file)
			continue
		}
		if _, ok := byPath[childPath]; !ok {
			byPath[childPath] = &corev1.TreeEntry{Path: childPath, Name: parts[0], Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY}
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
	return out
}
