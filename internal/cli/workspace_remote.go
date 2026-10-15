package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func (r Runner) remoteWorkspaceFiles(ctx context.Context, repo corev1.RepositoryServiceClient, ws WorkspaceConfig, commitID string) (map[string]remoteWorkspaceFile, error) {
	out := map[string]remoteWorkspaceFile{}
	for _, includedPath := range ws.IncludedPaths {
		if err := r.collectRemoteWorkspacePath(ctx, repo, ws, commitID, includedPath, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r Runner) collectRemoteWorkspacePath(ctx context.Context, repo corev1.RepositoryServiceClient, ws WorkspaceConfig, commitID, globalPath string, out map[string]remoteWorkspaceFile) error {
	resolved, err := repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: commitID, Path: globalPath})
	if grpcstatus.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if resolved.Entry == nil {
		return nil
	}
	switch resolved.Entry.Kind {
	case corev1.EntryKind_ENTRY_KIND_FILE:
		rel, err := workspaceRelPath(ws, resolved.Entry.Path)
		if err != nil {
			return err
		}
		out[resolved.Entry.Path] = remoteWorkspaceFile{
			BaseSnapshotFile: BaseSnapshotFile{
				Path:        resolved.Entry.Path,
				RelPath:     rel,
				ContentHash: resolved.Entry.ContentHash,
				Mode:        normalizedSnapshotMode(resolved.Entry.Mode),
				Size:        resolved.Entry.Size,
			},
			Entry: resolved.Entry,
		}
	case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
		entries, err := listDirectoryAll(ctx, repo, &corev1.ListDirectoryRequest{
			CommitId: commitID,
			Path:     resolved.Entry.Path,
			PageSize: 1000,
			Slice:    &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice},
		})
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			if err := r.collectRemoteWorkspacePath(ctx, repo, ws, commitID, entry.Path, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func remoteBaseSnapshotFiles(files map[string]remoteWorkspaceFile) map[string]BaseSnapshotFile {
	out := make(map[string]BaseSnapshotFile, len(files))
	for p, file := range files {
		out[p] = file.BaseSnapshotFile
	}
	return out
}

func removeWorkspaceFile(root string, ws WorkspaceConfig, current map[string]workingFile, globalPath string) error {
	if file, ok := current[globalPath]; ok && file.AbsPath != "" {
		err := os.Remove(file.AbsPath)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	rel, err := workspaceRelPath(ws, globalPath)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r Runner) writeWorkspaceRemoteFile(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, root string, ws WorkspaceConfig, current map[string]workingFile, commitID string, file remoteWorkspaceFile) error {
	data, exists, err := r.remoteFileBytes(ctx, repo, cache, commitID, file.BaseSnapshotFile, true)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	target := ""
	if local, ok := current[file.Path]; ok && local.AbsPath != "" {
		target = local.AbsPath
	} else {
		rel, err := workspaceRelPath(ws, file.Path)
		if err != nil {
			return err
		}
		target = filepath.Join(root, filepath.FromSlash(rel))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, fileModeFromSnapshot(file.Mode))
}

func (r Runner) writeWorkspaceMergedFile(root string, ws WorkspaceConfig, current map[string]workingFile, file mergedWorkspaceFile) error {
	target := ""
	if local, ok := current[file.Path]; ok && local.AbsPath != "" {
		target = local.AbsPath
	} else {
		rel, err := workspaceRelPath(ws, file.Path)
		if err != nil {
			return err
		}
		target = filepath.Join(root, filepath.FromSlash(rel))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, file.Data, fileModeFromSnapshot(file.Mode))
}
