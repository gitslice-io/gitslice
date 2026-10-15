package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type RepositoryService struct {
	Auth        storage.AuthStore
	Blobs       storage.BlobStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	ObjectStore ObjectStore
	// Blames stores blame snapshots (design/27_blame.md); optional.
	Blames    storage.BlameStore
	validator diffValidator
}

func (s *RepositoryService) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	subjectID := optionalSubject(ctx)
	p, err := repositoryReadPath(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if err := ensureRepositoryPathInSlice(slice, p); err != nil {
			return nil, err
		}
	} else {
		if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
			return nil, err
		}
	}
	req = cloneResolvePathRequest(req)
	req.Path = p
	return resolvePath(ctx, s.Repository, req)
}

func resolvePath(ctx context.Context, repository storage.RepositoryStore, req *corev1.ResolvePathRequest) (*corev1.ResolvePathResponse, error) {
	p, err := repositoryReadPath(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if p == "/" {
		entries, err := repositoryListDirectoryAtSource(ctx, repository, req.CommitId, req.RootTreeId, p)
		if err != nil {
			return nil, grpcError(err)
		}
		if len(entries) == 0 {
			return nil, status.Error(codes.NotFound, "path not found")
		}
		return &corev1.ResolvePathResponse{Entry: &corev1.TreeEntry{
			Path:   p,
			Name:   path.Base(p),
			Kind:   corev1.EntryKind_ENTRY_KIND_DIRECTORY,
			TreeId: directoryTreeIDFromEntries(entries),
		}}, nil
	}
	entry, err := repositoryGetEntryAtSource(ctx, repository, req.CommitId, req.RootTreeId, p)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ResolvePathResponse{Entry: treeEntryFromRepositoryEntry(*entry)}, nil
}

// repositoryReadPath normalizes paths used by repository browsing APIs.
//
// This intentionally accepts "/" and account-level ancestors such as "/acme".
// Repository directory reads can browse through ancestors that are not valid
// file mutation targets. Do not replace this with paths.Canonical: that helper
// enforces account/slice depth and is appropriate for committed file paths,
// workspace edits, and slice included paths, not for directory navigation.
func repositoryReadPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "/", nil
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path must be absolute: %s", p)
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "/", nil
	}
	return cleaned, nil
}

func repositoryGetEntryAtSource(ctx context.Context, repository storage.RepositoryStore, commitID, rootTreeID, p string) (*storage.TreeEntry, error) {
	rootTreeID = strings.TrimSpace(rootTreeID)
	if rootTreeID != "" {
		return repository.GetEntryAtTree(ctx, rootTreeID, p)
	}
	return repository.GetEntry(ctx, commitID, p)
}

func repositoryListDirectoryAtSource(ctx context.Context, repository storage.RepositoryStore, commitID, rootTreeID, p string) ([]storage.TreeEntry, error) {
	rootTreeID = strings.TrimSpace(rootTreeID)
	if rootTreeID != "" {
		return repository.ListDirectoryAtTree(ctx, rootTreeID, p)
	}
	return repository.ListDirectory(ctx, commitID, p)
}

func repositoryGetFileAtSource(ctx context.Context, repository storage.RepositoryStore, commitID, rootTreeID, p string) (*storage.FileEntry, error) {
	rootTreeID = strings.TrimSpace(rootTreeID)
	if rootTreeID != "" {
		return repository.GetFileAtTree(ctx, rootTreeID, p)
	}
	return repository.GetFile(ctx, commitID, p)
}

func cloneResolvePathRequest(req *corev1.ResolvePathRequest) *corev1.ResolvePathRequest {
	if req == nil {
		return &corev1.ResolvePathRequest{}
	}
	return proto.Clone(req).(*corev1.ResolvePathRequest)
}

func (s *RepositoryService) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	refName := req.RefName
	if refName == "" {
		refName = storage.DefaultTargetRef
	}
	ref, err := s.Repository.GetRef(ctx, refName)
	if err != nil {
		return nil, grpcError(err)
	}
	return ref, nil
}
