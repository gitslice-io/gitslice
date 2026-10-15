package cli

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type snapshotEditsStats struct {
	Scan         workspaceScanStats
	Blobs        blobAttachStats
	DiffDuration time.Duration
}

func changedPathsFromSnapshot(base BaseSnapshot, current map[string]workingFile) []string {
	pathSet := map[string]struct{}{}
	for p := range base.Files {
		pathSet[p] = struct{}{}
	}
	for p := range current {
		pathSet[p] = struct{}{}
	}
	out := make([]string, 0, len(pathSet))
	for p := range pathSet {
		baseFile, baseOK := base.Files[p]
		localFile, localOK := current[p]
		if !sameSnapshotFile(baseFile, baseOK, localFile.BaseSnapshotFile, localOK) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func workingFilesFromSnapshot(snapshot BaseSnapshot) map[string]workingFile {
	out := make(map[string]workingFile, len(snapshot.Files))
	for p, file := range snapshot.Files {
		out[p] = workingFile{BaseSnapshotFile: file}
	}
	return out
}

func sameSnapshotFile(a BaseSnapshotFile, aOK bool, b BaseSnapshotFile, bOK bool) bool {
	if aOK != bOK {
		return false
	}
	if !aOK {
		return true
	}
	return a.ContentHash == b.ContentHash && normalizedSnapshotMode(a.Mode) == normalizedSnapshotMode(b.Mode)
}

func normalizedSnapshotMode(mode uint32) uint32 {
	if mode == 0 {
		return 0o100644
	}
	return mode
}

func fileModeFromSnapshot(mode uint32) fs.FileMode {
	if normalizedSnapshotMode(mode)&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func (r Runner) snapshotEdits(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, upload bool) ([]*corev1.FileEdit, map[string]workingFile, error) {
	edits, current, _, err := r.snapshotEditsWithStats(ctx, conn, cfg, ws, upload, defaultCaptureHashConcurrency(), defaultUploadConcurrency())
	return edits, current, err
}

func (r Runner) snapshotEditsWithStats(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, upload bool, hashConcurrency, uploadConcurrency int) ([]*corev1.FileEdit, map[string]workingFile, snapshotEditsStats, error) {
	base, err := r.readBaseSnapshot()
	if err != nil {
		return nil, nil, snapshotEditsStats{}, err
	}
	return r.snapshotEditsAgainstBaseWithStats(ctx, conn, cfg, ws, base, upload, hashConcurrency, uploadConcurrency)
}

func (r Runner) snapshotEditsAgainstBase(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, base BaseSnapshot, upload bool) ([]*corev1.FileEdit, map[string]workingFile, error) {
	edits, current, _, err := r.snapshotEditsAgainstBaseWithStats(ctx, conn, cfg, ws, base, upload, defaultCaptureHashConcurrency(), defaultUploadConcurrency())
	return edits, current, err
}

func (r Runner) snapshotEditsAgainstBaseWithStats(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, base BaseSnapshot, upload bool, hashConcurrency, uploadConcurrency int) ([]*corev1.FileEdit, map[string]workingFile, snapshotEditsStats, error) {
	var stats snapshotEditsStats
	current, scanStats, err := r.scanWorkspaceFilesWithStats(ctx, ws, hashConcurrency, base.Files)
	stats.Scan = scanStats
	if err != nil {
		return nil, nil, stats, err
	}
	var blobClient corev1.BlobServiceClient
	callCtx := ctx
	if upload {
		if conn == nil {
			return nil, nil, stats, fmt.Errorf("connection is required for blob upload")
		}
		blobClient = corev1.NewBlobServiceClient(conn)
		callCtx = authContext(ctx, cfg)
	}
	cache, err := r.objectCache()
	if err != nil {
		return nil, nil, stats, err
	}
	diffStarted := time.Now()
	var edits []*corev1.FileEdit
	for p, file := range current {
		baseFile, ok := base.Files[p]
		if ok && baseFile.ContentHash == file.ContentHash && sameWorkspaceMode(baseFile.Mode, file.Mode) {
			continue
		}
		edit := &corev1.FileEdit{Op: "upsert", Path: p, ContentHash: file.ContentHash, Mode: file.Mode}
		edits = append(edits, edit)
	}
	for p := range base.Files {
		if _, ok := current[p]; !ok {
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: p})
		}
	}
	stats.DiffDuration = time.Since(diffStarted)
	if upload {
		blobStats, err := attachBlobIDsWithStats(callCtx, blobClient, &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, cache, edits, uploadConcurrency)
		stats.Blobs = blobStats
		if err != nil {
			return edits, current, stats, err
		}
	}
	sortFileEdits(edits)
	return edits, current, stats, nil
}

// sameWorkspaceMode compares a base mode with a scanned one. A symlink the
// workspace had to materialize as a regular file (an absolute target, or a
// filesystem without symlinks) still counts as unchanged.
func sameWorkspaceMode(base, local uint32) bool {
	if base == local {
		return true
	}
	return isSymlinkMode(base) && local == 0o100644
}

func (r Runner) stackBaseSnapshotThroughChangeset(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, stack *corev1.ChangesetStack, throughChangesetID string) (BaseSnapshot, error) {
	base, err := r.readBaseSnapshot()
	if err != nil {
		return BaseSnapshot{}, err
	}
	if base.Files == nil {
		base.Files = map[string]BaseSnapshotFile{}
	}
	if stack == nil || strings.TrimSpace(throughChangesetID) == "" {
		return base, nil
	}
	if base.CommitID != "" && stack.BaseCommitId != "" && base.CommitID != stack.BaseCommitId {
		return BaseSnapshot{}, userError(
			"workspace_base_mismatch",
			"workspace base does not match the active dependency tree base",
			"Run gs sync before editing these dependencies, or initialize a fresh workspace.",
		)
	}
	entries := map[string]*corev1.ChangesetStackEntry{}
	for _, entry := range stack.Entries {
		if entry == nil || entry.ChangesetId == "" {
			continue
		}
		entries[entry.ChangesetId] = entry
	}
	entry := entries[strings.TrimSpace(throughChangesetID)]
	if entry == nil {
		return BaseSnapshot{}, userError("stack_entry_not_found", "changeset not found", "Run gs deps to inspect available changesets.")
	}
	pathEntries := []*corev1.ChangesetStackEntry{}
	for entry != nil {
		pathEntries = append(pathEntries, entry)
		if entry.ParentChangesetId == "" {
			break
		}
		entry = entries[entry.ParentChangesetId]
	}
	for i, j := 0, len(pathEntries)-1; i < j; i, j = i+1, j-1 {
		pathEntries[i], pathEntries[j] = pathEntries[j], pathEntries[i]
	}
	changesetClient := corev1.NewChangesetServiceClient(conn)
	callCtx := authContext(ctx, cfg)
	for _, pathEntry := range pathEntries {
		cs := pathEntry.Changeset
		if cs == nil {
			var err error
			cs, err = changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: pathEntry.ChangesetId})
			if err != nil {
				return BaseSnapshot{}, err
			}
		}
		if err := applyCurrentPatchsetToSnapshot(ws, &base, cs); err != nil {
			return BaseSnapshot{}, err
		}
	}
	return base, nil
}

func applyCurrentPatchsetToSnapshot(ws WorkspaceConfig, base *BaseSnapshot, cs *corev1.Changeset) error {
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil
	}
	return applyFileEditsToSnapshot(ws, base, patchset.FileEdits)
}

func applyFileEditsToSnapshot(ws WorkspaceConfig, base *BaseSnapshot, edits []*corev1.FileEdit) error {
	if base.Files == nil {
		base.Files = map[string]BaseSnapshotFile{}
	}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		switch edit.Op {
		case "delete":
			deleteSnapshotPath(base.Files, edit.Path)
		case "rename":
			if err := renameSnapshotPath(ws, base.Files, edit.OldPath, edit.Path); err != nil {
				return err
			}
		case "mkdir":
			continue
		default:
			if edit.ContentHash == "" {
				return userError("invalid_patchset", "patchset file edit has no content hash", "Run gs update-dependents before switching or modifying it.")
			}
			rel, err := workspaceRelPath(ws, edit.Path)
			if err != nil {
				return err
			}
			mode := edit.Mode
			if mode == 0 {
				mode = 0o100644
			}
			base.Files[edit.Path] = BaseSnapshotFile{
				Path:        edit.Path,
				RelPath:     rel,
				ContentHash: edit.ContentHash,
				Mode:        mode,
			}
		}
	}
	return nil
}

func deleteSnapshotPath(files map[string]BaseSnapshotFile, p string) {
	delete(files, p)
	prefix := strings.TrimRight(p, "/") + "/"
	for existing := range files {
		if strings.HasPrefix(existing, prefix) {
			delete(files, existing)
		}
	}
}

func renameSnapshotPath(ws WorkspaceConfig, files map[string]BaseSnapshotFile, oldPath, newPath string) error {
	oldPrefix := strings.TrimRight(oldPath, "/") + "/"
	for existing, file := range files {
		nextPath := ""
		switch {
		case existing == oldPath:
			nextPath = newPath
		case strings.HasPrefix(existing, oldPrefix):
			nextPath = strings.TrimRight(newPath, "/") + strings.TrimPrefix(existing, strings.TrimRight(oldPath, "/"))
		default:
			continue
		}
		delete(files, existing)
		rel, err := workspaceRelPath(ws, nextPath)
		if err != nil {
			return err
		}
		file.Path = nextPath
		file.RelPath = rel
		files[nextPath] = file
	}
	return nil
}
