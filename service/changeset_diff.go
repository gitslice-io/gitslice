package service

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *ChangesetService) DiffChangeset(ctx context.Context, req *corev1.DiffChangesetRequest) (*corev1.DiffChangesetResponse, error) {
	subjectID := optionalSubject(ctx)
	cs, err := s.getAuthorizedChangeset(ctx, subjectID, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	toSelector := req.Patchset
	if toSelector == "" {
		toSelector = req.ToPatchset
	}
	toPatchset, err := selectChangesetPatchset(cs, toSelector)
	if err != nil {
		return nil, err
	}
	var fromPatchset *corev1.Patchset
	if req.FromPatchset != "" {
		fromPatchset, err = selectChangesetPatchset(cs, req.FromPatchset)
		if err != nil {
			return nil, err
		}
	}
	paths := changedPathsForDiff(fromPatchset, toPatchset)
	fromID := ""
	if fromPatchset != nil {
		fromID = fromPatchset.Id
	}
	response := &corev1.DiffChangesetResponse{
		ChangesetId:    cs.Id,
		FromPatchsetId: fromID,
		ToPatchsetId:   toPatchset.Id,
		ChangedPaths:   paths,
	}

	diffPaths := paths
	fullDiff := len(req.Paths) == 0
	cacheKey := ""
	if fullDiff {
		cacheKey = diffCacheKey(cs.Id, fromID, toPatchset.Id)
		if diff, ok := s.readDiffCache(ctx, cacheKey); ok {
			response.Diff = diff
			return response, nil
		}
	} else {
		requested := make(map[string]struct{}, len(req.Paths))
		for _, p := range req.Paths {
			requested[p] = struct{}{}
		}
		diffPaths = make([]string, 0, len(paths))
		for _, p := range paths {
			if _, ok := requested[p]; ok {
				diffPaths = append(diffPaths, p)
			}
		}
	}

	chunks := make([]string, len(diffPaths))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(diffFileConcurrency)
	for i, p := range diffPaths {
		i, p := i, p
		group.Go(func() error {
			oldFile, newFile, err := s.diffFileSides(groupCtx, fromPatchset, toPatchset, p)
			if err != nil {
				return err
			}
			chunks[i] = diffutil.UnifiedFileDiff(oldFile, newFile)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	var out strings.Builder
	for _, chunk := range chunks {
		if chunk != "" {
			out.WriteString(chunk)
		}
	}
	response.Diff = out.String()
	if fullDiff {
		s.writeDiffCache(ctx, cacheKey, response.Diff)
	}
	return response, nil
}

func changedPathsForDiff(from, to *corev1.Patchset) []string {
	seen := map[string]struct{}{}
	for _, patchset := range []*corev1.Patchset{from, to} {
		if patchset == nil {
			continue
		}
		for _, edit := range patchset.FileEdits {
			for _, p := range editPaths(edit) {
				seen[p] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type diffBaseFileKey struct {
	commitID string
	treeID   string
	path     string
}

type diffContentReader func(context.Context, string) ([]byte, error)

type diffBaseFileReader func(context.Context, string, string, string) (diffutil.File, error)

func (s *ChangesetService) diffFileSides(ctx context.Context, from, to *corev1.Patchset, p string) (diffutil.File, diffutil.File, error) {
	contentByHash := map[string][]byte{}
	readContent := func(ctx context.Context, contentHash string) ([]byte, error) {
		if data, ok := contentByHash[contentHash]; ok {
			return data, nil
		}
		data, err := s.readContentHash(ctx, contentHash)
		if err != nil {
			return nil, err
		}
		contentByHash[contentHash] = data
		return data, nil
	}

	// The object-store cache is write-through and does not cache read misses, so
	// this request-local dedup is what removes duplicate base fetches while
	// resolving the old and new sides for one changed path.
	baseFiles := map[diffBaseFileKey]diffutil.File{}
	readBase := func(ctx context.Context, commitID, treeID, p string) (diffutil.File, error) {
		key := diffBaseFileKey{commitID: commitID, treeID: treeID, path: p}
		if file, ok := baseFiles[key]; ok {
			return file, nil
		}
		file, err := s.baseFileWithReader(ctx, commitID, treeID, p, readContent)
		if err != nil {
			return diffutil.File{}, err
		}
		baseFiles[key] = file
		return file, nil
	}

	var oldFile diffutil.File
	var err error
	if from == nil {
		oldFile, err = readBase(ctx, to.BaseCommitId, to.BaseTreeId, p)
	} else {
		oldFile, err = s.patchsetFileWithReaders(ctx, from, p, readBase, readContent)
	}
	if err != nil {
		return diffutil.File{}, diffutil.File{}, err
	}
	newFile, err := s.patchsetFileWithReaders(ctx, to, p, readBase, readContent)
	if err != nil {
		return diffutil.File{}, diffutil.File{}, err
	}
	return oldFile, newFile, nil
}

func (s *ChangesetService) patchsetFile(ctx context.Context, patchset *corev1.Patchset, p string) (diffutil.File, error) {
	return s.patchsetFileWithReaders(ctx, patchset, p, func(ctx context.Context, commitID, treeID, p string) (diffutil.File, error) {
		return s.baseFileWithReader(ctx, commitID, treeID, p, s.readContentHash)
	}, s.readContentHash)
}

func (s *ChangesetService) patchsetFileWithReaders(ctx context.Context, patchset *corev1.Patchset, p string, readBase diffBaseFileReader, readContent diffContentReader) (diffutil.File, error) {
	var file diffutil.File
	fileSet := false
	for _, edit := range patchset.FileEdits {
		switch edit.Op {
		case "delete":
			if edit.Path == p {
				file = diffutil.File{Path: p}
				fileSet = true
			}
		case "rename":
			if edit.OldPath == p {
				file = diffutil.File{Path: p}
				fileSet = true
			}
			if edit.Path == p {
				if edit.BlobId != "" || edit.ContentHash != "" {
					return s.editFileWithReader(ctx, edit, p, readContent)
				}
				oldFile, err := readBase(ctx, patchset.BaseCommitId, patchset.BaseTreeId, edit.OldPath)
				if err != nil {
					return diffutil.File{}, err
				}
				oldFile.Path = p
				file = oldFile
				fileSet = true
			}
		case "upsert", "add", "update":
			if edit.Path == p {
				return s.editFileWithReader(ctx, edit, p, readContent)
			}
		}
	}
	if !fileSet {
		return readBase(ctx, patchset.BaseCommitId, patchset.BaseTreeId, p)
	}
	return file, nil
}

func (s *ChangesetService) baseFile(ctx context.Context, commitID, p string) (diffutil.File, error) {
	return s.baseFileWithReader(ctx, commitID, "", p, s.readContentHash)
}

func (s *ChangesetService) baseFileWithReader(ctx context.Context, commitID, treeID, p string, readContent diffContentReader) (diffutil.File, error) {
	var entry *storage.FileEntry
	var err error
	if treeID != "" {
		entry, err = s.Repository.GetFileAtTree(ctx, treeID, p)
	} else {
		entry, err = s.Repository.GetFile(ctx, commitID, p)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return diffutil.File{Path: p}, nil
	}
	if err != nil {
		return diffutil.File{}, grpcError(err)
	}
	data, err := readContent(ctx, entry.ContentHash)
	if err != nil {
		return diffutil.File{}, err
	}
	return diffutil.File{Path: p, Exists: true, Data: data}, nil
}

func (s *ChangesetService) editFile(ctx context.Context, edit *corev1.FileEdit, p string) (diffutil.File, error) {
	return s.editFileWithReader(ctx, edit, p, s.readContentHash)
}

func (s *ChangesetService) editFileWithReader(ctx context.Context, edit *corev1.FileEdit, p string, readContent diffContentReader) (diffutil.File, error) {
	contentHash := edit.ContentHash
	if contentHash == "" && edit.BlobId != "" {
		blob, err := s.Blobs.GetByID(ctx, edit.BlobId)
		if err != nil {
			return diffutil.File{}, grpcError(err)
		}
		contentHash = blob.ContentHash
	}
	if contentHash == "" {
		return diffutil.File{}, status.Errorf(codes.FailedPrecondition, "edit for %s has no content hash", p)
	}
	data, err := readContent(ctx, contentHash)
	if err != nil {
		return diffutil.File{}, err
	}
	return diffutil.File{Path: p, Exists: true, Data: data}, nil
}

func (s *ChangesetService) readContentHash(ctx context.Context, contentHash string) ([]byte, error) {
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, grpcError(err)
	}
	return data, nil
}
