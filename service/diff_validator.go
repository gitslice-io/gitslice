package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (v diffValidator) validateFileEdits(ctx context.Context, slice *corev1.Slice, baseCommitID, baseTreeID string, edits []*corev1.FileEdit, requireBlob bool) (*corev1.ValidateWorkspaceDiffResponse, error) {
	changed := map[string]struct{}{}
	normalizedEdits := make([]*corev1.FileEdit, len(edits))
	for i, edit := range edits {
		normalized, err := normalizeEdit(edit, requireBlob)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		normalizedEdits[i] = normalized
	}
	if requireBlob {
		if err := v.hydrateBlobsMetadata(ctx, normalizedEdits); err != nil {
			return nil, err
		}
	}
	// Path bases are read from the base tree, resolved once, rather than
	// looked up path by path.
	if baseTreeID == "" && baseCommitID != "" {
		if rootTreeID, err := v.Repository.RootTreeForCommit(ctx, baseCommitID); err == nil {
			baseTreeID = rootTreeID
		}
	}
	for i, edit := range edits {
		normalized := normalizedEdits[i]
		edit.Op = normalized.Op
		edit.Path = normalized.Path
		edit.OldPath = normalized.OldPath
		edit.BlobId = normalized.BlobId
		edit.ContentHash = normalized.ContentHash
		edit.Mode = normalized.Mode
		for _, p := range editPaths(edit) {
			if !paths.InAnyPrefix(slice.Definition.IncludedPaths, p) {
				return nil, status.Errorf(codes.FailedPrecondition, "path %s is outside slice %s/%s", p, slice.Ref.Account, slice.Ref.Slice)
			}
			changed[p] = struct{}{}
		}
	}
	affected := make([]string, 0, len(changed))
	for p := range changed {
		affected = append(affected, p)
	}
	sort.Strings(affected)
	coverageByPath, err := v.Slices.CoveringIDsByPath(ctx, affected)
	if err != nil {
		return nil, grpcError(err)
	}
	coverage := make([]*corev1.PathCoverage, 0, len(affected))
	pathBases := make([]*corev1.PathBase, 0, len(affected))
	readSet := make([]*corev1.PathSetEntry, 0, len(affected))
	writeSet := make([]*corev1.PathSetEntry, 0, len(affected))
	for _, p := range affected {
		coverage = append(coverage, &corev1.PathCoverage{Path: p, CoveringSliceIds: coverageByPath[p]})
		base, err := v.pathBase(ctx, baseCommitID, baseTreeID, p)
		if err != nil {
			return nil, err
		}
		pathBases = append(pathBases, base)
		readSet = append(readSet, &corev1.PathSetEntry{Path: p})
		writeSet = append(writeSet, &corev1.PathSetEntry{Path: p})
	}
	return &corev1.ValidateWorkspaceDiffResponse{
		AffectedPaths: affected,
		Coverage:      coverage,
		SubmitRequirements: &corev1.SubmitRequirements{
			RequiredApprovals: slice.Definition.RequiredApprovals,
			RequiredChecks:    append([]string(nil), slice.Definition.RequiredChecks...),
			SourceSliceDefinitionHash: storage.SubmitRequirementsHash(
				slice.Definition.IncludedPaths,
				slice.Definition.RequiredApprovals,
				slice.Definition.RequiredChecks,
			),
		},
		PathBases: pathBases,
		ReadSet:   readSet,
		WriteSet:  writeSet,
	}, nil
}

// hydrateBlobsMetadata sets each edit's content hash from its blob, reading
// the blobs in one query.
func (v diffValidator) hydrateBlobsMetadata(ctx context.Context, edits []*corev1.FileEdit) error {
	var ids []string
	for _, edit := range edits {
		if edit != nil && edit.Op != "delete" && edit.Op != "rename" && edit.Op != "mkdir" {
			ids = append(ids, edit.BlobId)
		}
	}
	blobs, err := v.Blobs.GetByIDs(ctx, ids)
	if err != nil {
		return grpcError(err)
	}
	for _, edit := range edits {
		if edit == nil || edit.Op == "delete" || edit.Op == "rename" || edit.Op == "mkdir" {
			continue
		}
		blob := blobs[edit.BlobId]
		if blob == nil {
			return grpcError(storage.ErrNotFound)
		}
		if edit.ContentHash != "" && edit.ContentHash != blob.ContentHash {
			return status.Errorf(codes.InvalidArgument, "content hash does not match blob %s", edit.BlobId)
		}
		edit.ContentHash = blob.ContentHash
	}
	return nil
}

func (v diffValidator) pathBase(ctx context.Context, baseCommitID, baseTreeID, p string) (*corev1.PathBase, error) {
	base := &corev1.PathBase{
		Path:             p,
		BaseCommitId:     baseCommitID,
		Check:            "entry_fingerprint",
		EntryFingerprint: storage.MissingEntryFingerprint(),
	}
	var entry *storage.TreeEntry
	var err error
	if baseTreeID != "" {
		entry, err = v.Repository.GetEntryAtTree(ctx, baseTreeID, p)
	} else {
		entry, err = v.Repository.GetEntry(ctx, baseCommitID, p)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return base, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	base.Exists = true
	base.EntryKind = entry.Kind
	switch entry.Kind {
	case "file":
		base.Mode = entry.Mode
		base.BlobId = entry.BlobID
		base.ContentHash = entry.ContentHash
		base.EntryFingerprint = storage.FileEntryFingerprint(storage.FileEntry{
			Path:        entry.Path,
			BlobID:      entry.BlobID,
			ContentHash: entry.ContentHash,
			Mode:        entry.Mode,
			Size:        entry.Size,
		})
	case "directory":
		base.TreeId = entry.TreeID
		base.EntryFingerprint = storage.DirectoryEntryFingerprint(entry.TreeID)
	default:
		base.EntryFingerprint = storage.MissingEntryFingerprint()
	}
	return base, nil
}

func normalizeEdit(edit *corev1.FileEdit, requireBlob bool) (*corev1.FileEdit, error) {
	if edit == nil {
		return nil, fmt.Errorf("file edit is nil")
	}
	out := &corev1.FileEdit{
		Op:          edit.Op,
		Path:        edit.Path,
		OldPath:     edit.OldPath,
		BlobId:      edit.BlobId,
		ContentHash: edit.ContentHash,
		Mode:        edit.Mode,
	}
	if out.Op == "" {
		out.Op = "upsert"
	}
	switch out.Op {
	case "add", "update":
		out.Op = "upsert"
	case "upsert", "delete", "rename", "mkdir":
	default:
		return nil, fmt.Errorf("unsupported file edit op %q", out.Op)
	}
	if out.Op == "rename" {
		if out.OldPath == "" {
			return nil, fmt.Errorf("old path is required for rename edit")
		}
		if out.Path == "" {
			return nil, fmt.Errorf("path is required for rename edit")
		}
	} else if out.Path == "" {
		return nil, fmt.Errorf("path is required for %s edit", out.Op)
	} else if out.OldPath != "" {
		return nil, fmt.Errorf("old path is only supported for rename edits")
	}
	if out.Path != "" {
		p, err := paths.Canonical(out.Path)
		if err != nil {
			return nil, err
		}
		out.Path = p
	}
	if out.OldPath != "" {
		p, err := paths.Canonical(out.OldPath)
		if err != nil {
			return nil, err
		}
		out.OldPath = p
	}
	if out.Op == "rename" && out.Path == out.OldPath {
		return nil, fmt.Errorf("rename source and destination must differ")
	}
	if requireBlob && out.Op != "delete" && out.Op != "rename" && out.Op != "mkdir" && out.BlobId == "" {
		return nil, fmt.Errorf("blob id is required for %s edit on %s", out.Op, out.Path)
	}
	if out.Mode == 0 && out.Op != "delete" && out.Op != "mkdir" {
		out.Mode = 0o100644
	}
	return out, nil
}

func editPaths(edit *corev1.FileEdit) []string {
	var out []string
	if edit.Path != "" {
		out = append(out, edit.Path)
	}
	if edit.OldPath != "" {
		out = append(out, edit.OldPath)
	}
	return out
}
