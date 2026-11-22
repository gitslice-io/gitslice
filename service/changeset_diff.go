package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxDiffFileBytes     = 4 << 20
	diffBinarySniffBytes = 8 << 10
	maxDiffOutputBytes   = 16 << 20
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
	return s.diffLoadedChangeset(ctx, cs, req.FromPatchset, toSelector, req.Paths, "", maxDiffOutputBytes)
}

func (s *ChangesetService) diffLoadedChangeset(
	ctx context.Context,
	cs *corev1.Changeset,
	fromSelector, toSelector string,
	requestedPaths []string,
	accountRoot string,
	maxOutputBytes int,
) (*corev1.DiffChangesetResponse, error) {
	if maxOutputBytes <= 0 {
		maxOutputBytes = maxDiffOutputBytes
	}
	toPatchset, err := selectChangesetPatchset(cs, toSelector)
	if err != nil {
		return nil, err
	}
	var fromPatchset *corev1.Patchset
	if fromSelector != "" {
		fromPatchset, err = selectChangesetPatchset(cs, fromSelector)
		if err != nil {
			return nil, err
		}
	}
	paths := changedPathsForDiff(fromPatchset, toPatchset)
	if accountRoot != "" {
		prefix := strings.TrimRight(accountRoot, "/") + "/"
		scoped := make([]string, 0, len(paths))
		for _, p := range paths {
			if p == accountRoot || strings.HasPrefix(p, prefix) {
				scoped = append(scoped, p)
			}
		}
		paths = scoped
	}
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
	fullDiff := len(requestedPaths) == 0 && accountRoot == "" && maxOutputBytes == maxDiffOutputBytes
	cacheKey := ""
	if fullDiff {
		cacheKey = diffCacheKey(cs.Id, fromID, toPatchset.Id)
		if diff, ok := s.readDiffCache(ctx, cacheKey); ok {
			response.Diff = diff
			return response, nil
		}
	}
	if len(requestedPaths) > 0 {
		requested := make(map[string]struct{}, len(requestedPaths))
		for _, p := range requestedPaths {
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
			chunk, err := s.diffSinglePath(groupCtx, fromPatchset, toPatchset, p)
			if err != nil {
				return err
			}
			chunks[i] = chunk
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	var out strings.Builder
	truncated := false
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		if out.Len()+len(chunk) > maxOutputBytes {
			truncated = true
			break
		}
		out.WriteString(chunk)
	}
	if truncated {
		out.WriteString("# diff output truncated: exceeds total diff size limit\n")
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
			if edit.GetOp() == "copy" {
				// The source of a copy is read, not changed.
				seen[edit.Path] = struct{}{}
				continue
			}
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

type diffBlobData struct {
	data      []byte
	oversized bool
	binary    bool
}

type diffSide struct {
	file        diffutil.File
	contentHash string
	oversized   bool
	binary      bool
}

type diffContentReader func(ctx context.Context, contentHash string, knownSize int64) (diffBlobData, error)

type diffBaseFileReader func(context.Context, string, string, string) (diffSide, error)

func (s *ChangesetService) diffSinglePath(ctx context.Context, from, to *corev1.Patchset, p string) (string, error) {
	oldSide, newSide, err := s.diffPathSides(ctx, from, to, p)
	if err != nil {
		return "", err
	}
	if !oldSide.oversized && !newSide.oversized {
		return diffutil.UnifiedFileDiff(oldSide.file, newSide.file), nil
	}
	if oldSide.file.Exists && newSide.file.Exists && oldSide.contentHash != "" && oldSide.contentHash == newSide.contentHash {
		return "", nil
	}
	label := strings.TrimPrefix(p, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", label, label)
	if !oldSide.file.Exists && newSide.file.Exists {
		b.WriteString("new file mode 100644\n")
	}
	if oldSide.file.Exists && !newSide.file.Exists {
		b.WriteString("deleted file mode 100644\n")
	}
	if (oldSide.file.Exists && oldSide.binary) || (newSide.file.Exists && newSide.binary) {
		oldLabel := "/dev/null"
		if oldSide.file.Exists {
			oldLabel = "a/" + label
		}
		newLabel := "/dev/null"
		if newSide.file.Exists {
			newLabel = "b/" + label
		}
		fmt.Fprintf(&b, "Binary files %s and %s differ\n", oldLabel, newLabel)
		return b.String(), nil
	}
	fmt.Fprintf(&b, "Diff too large to render: a/%s and b/%s differ (exceeds %d MiB per-file diff limit)\n", label, label, maxDiffFileBytes>>20)
	return b.String(), nil
}

func (s *ChangesetService) diffPathSides(ctx context.Context, from, to *corev1.Patchset, p string) (diffSide, diffSide, error) {
	contentByHash := map[string]diffBlobData{}
	readContent := func(ctx context.Context, contentHash string, knownSize int64) (diffBlobData, error) {
		if blob, ok := contentByHash[contentHash]; ok {
			return blob, nil
		}
		blob, err := s.readDiffContentHash(ctx, contentHash, knownSize)
		if err != nil {
			return diffBlobData{}, err
		}
		contentByHash[contentHash] = blob
		return blob, nil
	}

	// The object-store cache is write-through and does not cache read misses, so
	// this request-local dedup is what removes duplicate base fetches while
	// resolving the old and new sides for one changed path.
	baseFiles := map[diffBaseFileKey]diffSide{}
	readBase := func(ctx context.Context, commitID, treeID, p string) (diffSide, error) {
		key := diffBaseFileKey{commitID: commitID, treeID: treeID, path: p}
		if side, ok := baseFiles[key]; ok {
			return side, nil
		}
		side, err := s.baseFileWithReader(ctx, commitID, treeID, p, readContent)
		if err != nil {
			return diffSide{}, err
		}
		baseFiles[key] = side
		return side, nil
	}

	var oldSide diffSide
	var err error
	if from == nil {
		oldSide, err = readBase(ctx, to.BaseCommitId, to.BaseTreeId, p)
	} else {
		oldSide, err = s.patchsetFileWithReaders(ctx, from, p, readBase, readContent)
	}
	if err != nil {
		return diffSide{}, diffSide{}, err
	}
	newSide, err := s.patchsetFileWithReaders(ctx, to, p, readBase, readContent)
	if err != nil {
		return diffSide{}, diffSide{}, err
	}
	return oldSide, newSide, nil
}

func (s *ChangesetService) patchsetFileWithReaders(ctx context.Context, patchset *corev1.Patchset, p string, readBase diffBaseFileReader, readContent diffContentReader) (diffSide, error) {
	var side diffSide
	fileSet := false
	for _, edit := range patchset.FileEdits {
		switch edit.Op {
		case "delete":
			if edit.Path == p {
				side = diffSide{file: diffutil.File{Path: p}}
				fileSet = true
			}
		case "copy":
			if edit.Path == p {
				oldSide, err := readBase(ctx, patchset.BaseCommitId, patchset.BaseTreeId, edit.OldPath)
				if err != nil {
					return diffSide{}, err
				}
				oldSide.file.Path = p
				side = oldSide
				fileSet = true
			}
		case "rename":
			if edit.OldPath == p {
				side = diffSide{file: diffutil.File{Path: p}}
				fileSet = true
			}
			if edit.Path == p {
				if edit.BlobId != "" || edit.ContentHash != "" {
					return s.editFileWithReader(ctx, edit, p, readContent)
				}
				oldSide, err := readBase(ctx, patchset.BaseCommitId, patchset.BaseTreeId, edit.OldPath)
				if err != nil {
					return diffSide{}, err
				}
				oldSide.file.Path = p
				side = oldSide
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
	return side, nil
}

func (s *ChangesetService) baseFileWithReader(ctx context.Context, commitID, treeID, p string, readContent diffContentReader) (diffSide, error) {
	var entry *storage.FileEntry
	var err error
	if treeID != "" {
		entry, err = s.Repository.GetFileAtTree(ctx, treeID, p)
	} else {
		entry, err = s.Repository.GetFile(ctx, commitID, p)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return diffSide{file: diffutil.File{Path: p}}, nil
	}
	if err != nil {
		return diffSide{}, grpcError(err)
	}
	blob, err := readContent(ctx, entry.ContentHash, entry.Size)
	if err != nil {
		return diffSide{}, err
	}
	return diffSide{
		file:        diffutil.File{Path: p, Exists: true, Data: blob.data},
		contentHash: entry.ContentHash,
		oversized:   blob.oversized,
		binary:      blob.binary,
	}, nil
}

func (s *ChangesetService) editFileWithReader(ctx context.Context, edit *corev1.FileEdit, p string, readContent diffContentReader) (diffSide, error) {
	contentHash := edit.ContentHash
	var knownSize int64
	if contentHash == "" && edit.BlobId != "" {
		blob, err := s.Blobs.GetByID(ctx, edit.BlobId)
		if err != nil {
			return diffSide{}, grpcError(err)
		}
		contentHash = blob.ContentHash
		knownSize = blob.Size
	}
	if contentHash == "" {
		return diffSide{}, status.Errorf(codes.FailedPrecondition, "edit for %s has no content hash", p)
	}
	blob, err := readContent(ctx, contentHash, knownSize)
	if err != nil {
		return diffSide{}, err
	}
	return diffSide{
		file:        diffutil.File{Path: p, Exists: true, Data: blob.data},
		contentHash: contentHash,
		oversized:   blob.oversized,
		binary:      blob.binary,
	}, nil
}

func sniffDiffBinary(data []byte) bool {
	if len(data) > diffBinarySniffBytes {
		data = data[:diffBinarySniffBytes]
	}
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}

func (s *ChangesetService) readDiffContentHash(ctx context.Context, contentHash string, knownSize int64) (diffBlobData, error) {
	key := filesystem.BlobKey(contentHash)
	if knownSize > maxDiffFileBytes {
		rc, err := s.ObjectStore.Get(ctx, key, 0, diffBinarySniffBytes)
		if err != nil {
			return diffBlobData{}, grpcError(err)
		}
		defer rc.Close()
		sniff, err := io.ReadAll(io.LimitReader(rc, diffBinarySniffBytes))
		if err != nil {
			return diffBlobData{}, grpcError(err)
		}
		return diffBlobData{oversized: true, binary: sniffDiffBinary(sniff)}, nil
	}
	rc, err := s.ObjectStore.Get(ctx, key, 0, maxDiffFileBytes+1)
	if err != nil {
		return diffBlobData{}, grpcError(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxDiffFileBytes+1))
	if err != nil {
		return diffBlobData{}, grpcError(err)
	}
	if len(data) > maxDiffFileBytes {
		return diffBlobData{oversized: true, binary: sniffDiffBinary(data)}, nil
	}
	return diffBlobData{data: data, binary: sniffDiffBinary(data)}, nil
}
