package service

import (
	"context"
	"io"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *RepositoryService) ReadFile(ctx context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	subjectID := optionalSubject(ctx)
	p, err := paths.Canonical(req.Path)
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
	if err := s.ensureAuthorizedRootTree(ctx, subjectID, req.GetRootTreeId()); err != nil {
		return nil, err
	}
	req = cloneReadFileRequest(req)
	req.Path = p
	return readFile(ctx, s.Repository, s.ObjectStore, req)
}

const (
	readFilesMaxPaths    = 1000
	readFilesMaxBytes    = 32 << 20
	readFilesObjectReads = 64
)

// ReadFiles reads many files in one call (design/28_benchmarks.md): it
// authorizes once through the slice, or once per account, resolves the
// entries in the (cached) tree, and reads their objects concurrently.
func (s *RepositoryService) ReadFiles(ctx context.Context, req *corev1.ReadFilesRequest) (*corev1.ReadFilesResponse, error) {
	subjectID := optionalSubject(ctx)
	if len(req.GetPaths()) > readFilesMaxPaths {
		return nil, status.Errorf(codes.InvalidArgument, "read at most %d files per call", readFilesMaxPaths)
	}
	canonical := make([]string, len(req.GetPaths()))
	for i, raw := range req.GetPaths() {
		p, err := paths.Canonical(raw)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		canonical[i] = p
	}
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		for _, p := range canonical {
			if err := ensureRepositoryPathInSlice(slice, p); err != nil {
				return nil, err
			}
		}
	} else {
		reads := pathReads{auth: s.Auth, slices: s.Slices, subjectID: subjectID}
		for _, p := range canonical {
			if account := accountSlugForRepositoryPath(p); account != "" {
				if err := reads.check(ctx, account, p); err != nil {
					return nil, err
				}
			}
		}
	}

	// Resolve the root tree once, then walk it (its nodes are cached) for
	// each path, rather than looking each path up in the database.
	rootTreeID := strings.TrimSpace(req.GetRootTreeId())
	if rootTreeID != "" {
		if err := s.ensureAuthorizedRootTree(ctx, subjectID, rootTreeID); err != nil {
			return nil, err
		}
	} else {
		var err error
		if rootTreeID, err = s.Repository.RootTreeForCommit(ctx, req.GetCommitId()); err != nil {
			return nil, grpcError(err)
		}
	}
	entries := make([]*storage.FileEntry, len(canonical))
	var total int64
	for i, p := range canonical {
		entry, err := s.Repository.GetFileAtTree(ctx, rootTreeID, p)
		if err != nil {
			return nil, grpcError(err)
		}
		entries[i] = entry
		total += entry.Size
	}
	if total > readFilesMaxBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "these files total %d bytes, more than the %d a call may read; read fewer at once", total, readFilesMaxBytes)
	}

	files := make([]*corev1.ReadFilesEntry, len(entries))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(readFilesObjectReads)
	for i, entry := range entries {
		group.Go(func() error {
			rc, err := s.ObjectStore.Get(groupCtx, objectstore.BlobKey(entry.ContentHash), 0, 0)
			if err != nil {
				return grpcError(err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				return grpcError(err)
			}
			files[i] = &corev1.ReadFilesEntry{Path: canonical[i], Data: data, ContentHash: entry.ContentHash}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return &corev1.ReadFilesResponse{Files: files}, nil
}

func cloneReadFileRequest(req *corev1.ReadFileRequest) *corev1.ReadFileRequest {
	if req == nil {
		return &corev1.ReadFileRequest{}
	}
	return proto.Clone(req).(*corev1.ReadFileRequest)
}

const maxUnaryReadFileBytes = 64 << 20

func readFile(ctx context.Context, repository storage.RepositoryStore, objectStore ObjectStore, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	p, err := paths.Canonical(req.Path)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.Offset < 0 {
		return nil, status.Error(codes.InvalidArgument, "offset must be non-negative")
	}
	if req.Length < 0 {
		return nil, status.Error(codes.InvalidArgument, "length must be non-negative")
	}
	if req.Length > maxUnaryReadFileBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "read length %d exceeds %d byte unary limit", req.Length, maxUnaryReadFileBytes)
	}
	entry, err := repositoryGetFileAtSource(ctx, repository, req.CommitId, req.RootTreeId, p)
	if err != nil {
		return nil, grpcError(err)
	}
	if req.Length == 0 && entry.Size-req.Offset > maxUnaryReadFileBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "file %s (%d bytes from offset) exceeds %d byte unary read limit", p, entry.Size-req.Offset, maxUnaryReadFileBytes)
	}
	rc, err := objectStore.Get(ctx, objectstore.BlobKey(entry.ContentHash), req.Offset, req.Length)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxUnaryReadFileBytes+1))
	if err != nil {
		return nil, grpcError(err)
	}
	if int64(len(data)) > maxUnaryReadFileBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "file %s exceeds %d byte unary read limit", p, maxUnaryReadFileBytes)
	}
	return &corev1.ReadFileResponse{Data: data, Offset: req.Offset, ContentHash: entry.ContentHash}, nil
}
