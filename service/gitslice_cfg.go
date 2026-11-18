package service

import (
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/gitslicecfg"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func newGitsliceTreeReader(repo storage.RepositoryStore, objects ObjectStore, maxBytes int64, account string, notFoundErr error) gitslicecfg.TreeReader {
	var lookup gitslicecfg.LookupFileFunc
	if repo != nil {
		lookup = func(ctx context.Context, rootTreeID, storagePath string) (gitslicecfg.FileEntry, bool, error) {
			entry, err := repo.GetFileAtTree(ctx, rootTreeID, storagePath)
			if errors.Is(err, storage.ErrNotFound) {
				return gitslicecfg.FileEntry{}, false, nil
			}
			if err != nil {
				return gitslicecfg.FileEntry{}, false, err
			}
			return gitslicecfg.FileEntry{
				ContentHash: entry.ContentHash,
				Size:        entry.Size,
			}, true, nil
		}
	}
	return gitslicecfg.TreeReader{
		LookupFile:  lookup,
		Objects:     objects,
		MaxBytes:    maxBytes,
		Account:     account,
		NotFoundErr: notFoundErr,
	}
}

func targetRefHeadCommitID(ctx context.Context, repo storage.RepositoryStore, cs *corev1.Changeset) (string, error) {
	if repo == nil {
		return "", nil
	}
	var refName string
	if cs != nil {
		refName = strings.TrimSpace(cs.GetTargetRef())
	}
	if refName == "" {
		refName = storage.DefaultTargetRef
	}
	head, err := repo.GetRef(ctx, refName)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head.GetCommitId()), nil
}

func directoryChildrenAtCommit(ctx context.Context, repo storage.RepositoryStore, commitID, dirPath string) ([]string, error) {
	if repo == nil || strings.TrimSpace(commitID) == "" || strings.TrimSpace(dirPath) == "" {
		return nil, nil
	}
	if canonical, err := paths.Canonical(dirPath); err == nil {
		dirPath = canonical
	}
	entry, err := repo.GetEntry(ctx, commitID, dirPath)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && entry.Kind == "file") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	children, err := repo.ListFiles(ctx, commitID, dirPath)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(children))
	for _, child := range children {
		if child.Path != "" {
			out = append(out, child.Path)
		}
	}
	return out, nil
}

func collectPatchsetPaths(ctx context.Context, repo storage.RepositoryStore, baseCommitID, headCommitID string, patchset *corev1.Patchset) ([]string, error) {
	var listDir gitslicecfg.ListDirectoryFilesFunc
	if repo != nil {
		listDir = func(ctx context.Context, commitID, dirPath string) ([]string, error) {
			return directoryChildrenAtCommit(ctx, repo, commitID, dirPath)
		}
	}
	return gitslicecfg.CollectTouchedPaths(ctx, patchset, []string{baseCommitID, headCommitID}, listDir)
}

func patchsetTouchedGitslicePathAt(ctx context.Context, repo storage.RepositoryStore, headCommitID string, patchset *corev1.Patchset) (string, error) {
	allPaths, err := collectPatchsetPaths(ctx, repo, patchset.GetBaseCommitId(), headCommitID, patchset)
	if err != nil {
		return "", err
	}
	return gitslicecfg.FirstGitslicePath(allPaths), nil
}

func uniqueNonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
