package cli

import (
	"context"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type remoteWorkspaceFile struct {
	BaseSnapshotFile
	Entry *corev1.TreeEntry
}

type mergedWorkspaceFile struct {
	Path string
	Data []byte
	Mode uint32
}

type workspaceSyncPlan struct {
	RemoteUpserts []remoteWorkspaceFile
	RemoteDeletes []string
	MergedFiles   []mergedWorkspaceFile
	UpdatedPaths  []string
	LocalPaths    []string
	MergedPaths   []string
	Conflicts     []WorkspaceConflict
}

func buildWorkspaceSyncPlan(oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile) workspaceSyncPlan {
	pathSet := map[string]struct{}{}
	for p := range base.Files {
		pathSet[p] = struct{}{}
	}
	for p := range current {
		pathSet[p] = struct{}{}
	}
	for p := range remote {
		pathSet[p] = struct{}{}
	}
	allPaths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		allPaths = append(allPaths, p)
	}
	sort.Strings(allPaths)

	plan := workspaceSyncPlan{}
	for _, p := range allPaths {
		baseFile, baseOK := base.Files[p]
		localFile, localOK := current[p]
		remoteFile, remoteOK := remote[p]
		localSnapshot := localFile.BaseSnapshotFile
		remoteSnapshot := remoteFile.BaseSnapshotFile
		localChanged := !sameSnapshotFile(baseFile, baseOK, localSnapshot, localOK)
		remoteChanged := !sameSnapshotFile(baseFile, baseOK, remoteSnapshot, remoteOK)
		switch {
		case !localChanged && remoteChanged:
			plan.UpdatedPaths = append(plan.UpdatedPaths, p)
			if remoteOK {
				plan.RemoteUpserts = append(plan.RemoteUpserts, remoteFile)
			} else {
				plan.RemoteDeletes = append(plan.RemoteDeletes, p)
			}
		case localChanged && remoteChanged:
			if sameSnapshotFile(localSnapshot, localOK, remoteSnapshot, remoteOK) {
				plan.UpdatedPaths = append(plan.UpdatedPaths, p)
				continue
			}
			plan.LocalPaths = append(plan.LocalPaths, p)
			plan.Conflicts = append(plan.Conflicts, workspaceConflict(oldBaseCommitID, newBaseCommitID, p, baseFile, baseOK, localSnapshot, localOK, remoteSnapshot, remoteOK))
		case localChanged:
			plan.LocalPaths = append(plan.LocalPaths, p)
		}
	}
	return plan
}

func normalizeWorkspaceSyncMergeStrategy(strategy string) (string, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		return defaultWorkspaceSyncMergeStrategy, nil
	}
	switch strategy {
	case workspaceSyncMergeLine, workspaceSyncMergeManual, workspaceSyncMergeOurs, workspaceSyncMergeTheirs:
		return strategy, nil
	default:
		return "", userError("invalid_args", "invalid sync merge strategy "+strategy, "Use --merge line, --merge manual, --merge ours, or --merge theirs.")
	}
}

func (r Runner) applyWorkspaceSyncMergeStrategy(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, oldBaseCommitID, newBaseCommitID, strategy string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, plan *workspaceSyncPlan) error {
	if len(plan.Conflicts) == 0 {
		return nil
	}
	if strategy == workspaceSyncMergeManual {
		return nil
	}
	remaining := make([]WorkspaceConflict, 0, len(plan.Conflicts))
	for _, conflict := range plan.Conflicts {
		switch strategy {
		case workspaceSyncMergeOurs:
			continue
		case workspaceSyncMergeTheirs:
			remoteFile, remoteOK := remote[conflict.Path]
			if remoteOK {
				plan.RemoteUpserts = append(plan.RemoteUpserts, remoteFile)
			} else {
				plan.RemoteDeletes = append(plan.RemoteDeletes, conflict.Path)
			}
			plan.UpdatedPaths = appendUniqueString(plan.UpdatedPaths, conflict.Path)
			plan.LocalPaths = removeStringValue(plan.LocalPaths, conflict.Path)
		case workspaceSyncMergeLine:
			merged, ok, err := r.tryLineMergeWorkspaceConflict(ctx, repo, cache, oldBaseCommitID, newBaseCommitID, base, current, remote, conflict)
			if err != nil {
				return err
			}
			if !ok {
				// A rule in .gitslice/merge.yaml (read from the new base)
				// may resolve what the line merge could not.
				resolved, err := r.applySyncMergeRule(ctx, repo, cache, oldBaseCommitID, newBaseCommitID, base, current, remote, conflict, plan)
				if err != nil {
					return err
				}
				if !resolved {
					remaining = append(remaining, conflict)
				}
				continue
			}
			plan.MergedFiles = append(plan.MergedFiles, merged)
			plan.MergedPaths = appendUniqueString(plan.MergedPaths, conflict.Path)
		}
	}
	plan.Conflicts = remaining
	sort.Strings(plan.LocalPaths)
	sort.Strings(plan.UpdatedPaths)
	sort.Strings(plan.MergedPaths)
	return nil
}

func (r Runner) tryLineMergeWorkspaceConflict(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, conflict WorkspaceConflict) (mergedWorkspaceFile, bool, error) {
	baseFile, baseOK := base.Files[conflict.Path]
	localFile, localOK := current[conflict.Path]
	remoteFile, remoteOK := remote[conflict.Path]
	if !baseOK || !localOK || !remoteOK {
		return mergedWorkspaceFile{}, false, nil
	}
	baseMode := normalizedSnapshotMode(baseFile.Mode)
	localMode := normalizedSnapshotMode(localFile.Mode)
	remoteMode := normalizedSnapshotMode(remoteFile.Mode)
	if baseMode != localMode || baseMode != remoteMode {
		return mergedWorkspaceFile{}, false, nil
	}
	baseData, baseExists, err := r.remoteFileBytes(ctx, repo, cache, oldBaseCommitID, baseFile, true)
	if err != nil {
		return mergedWorkspaceFile{}, false, err
	}
	localData, localExists, err := localWorkspaceFileBytes(localFile, true)
	if err != nil {
		return mergedWorkspaceFile{}, false, err
	}
	remoteData, remoteExists, err := r.remoteFileBytes(ctx, repo, cache, newBaseCommitID, remoteFile.BaseSnapshotFile, true)
	if err != nil {
		return mergedWorkspaceFile{}, false, err
	}
	if !baseExists || !localExists || !remoteExists {
		return mergedWorkspaceFile{}, false, nil
	}
	if !isTextConflictSide(baseData, true) || !isTextConflictSide(localData, true) || !isTextConflictSide(remoteData, true) {
		return mergedWorkspaceFile{}, false, nil
	}
	merged, ok := mergeTextLines(baseData, localData, remoteData)
	if !ok {
		return mergedWorkspaceFile{}, false, nil
	}
	return mergedWorkspaceFile{
		Path: conflict.Path,
		Data: merged,
		Mode: localMode,
	}, true, nil
}

func prefetchSyncPlanBlobs(
	ctx context.Context,
	repo corev1.RepositoryServiceClient,
	blob corev1.BlobServiceClient,
	sliceRef *corev1.SliceRef,
	cache *clientcache.ObjectCache,
	oldBaseCommitID, newBaseCommitID string,
	base BaseSnapshot,
	remoteFiles map[string]remoteWorkspaceFile,
	plan workspaceSyncPlan,
	lazyMode bool,
) error {
	var oldTargets []remoteReadTarget
	var newTargets []remoteReadTarget
	if !lazyMode {
		for _, up := range plan.RemoteUpserts {
			if up.ContentHash != "" {
				newTargets = append(newTargets, remoteReadTarget{
					Path:        up.Path,
					ContentHash: up.ContentHash,
					Size:        up.Size,
				})
			}
		}
	}
	for _, c := range plan.Conflicts {
		if bf, ok := base.Files[c.Path]; ok && bf.ContentHash != "" {
			oldTargets = append(oldTargets, remoteReadTarget{
				Path:        bf.Path,
				ContentHash: bf.ContentHash,
				Size:        bf.Size,
			})
		}
		if rf, ok := remoteFiles[c.Path]; ok && rf.ContentHash != "" {
			newTargets = append(newTargets, remoteReadTarget{
				Path:        rf.Path,
				ContentHash: rf.ContentHash,
				Size:        rf.Size,
			})
		}
	}
	if len(oldTargets) > 0 {
		if _, err := prefetchRemoteFilesToCache(ctx, repo, blob, sliceRef, cache, oldBaseCommitID, oldTargets); err != nil {
			return err
		}
	}
	if len(newTargets) > 0 {
		if _, err := prefetchRemoteFilesToCache(ctx, repo, blob, sliceRef, cache, newBaseCommitID, newTargets); err != nil {
			return err
		}
	}
	return nil
}

// manifestRemoteFiles is a listing as sync's remote side.
func manifestRemoteFiles(ws WorkspaceConfig, manifest *lazyfs.Manifest) (map[string]remoteWorkspaceFile, error) {
	out := map[string]remoteWorkspaceFile{}
	var err error
	manifest.Files(func(entry lazyfs.BaseEntry) {
		rel, relErr := workspaceRelPath(ws, entry.Path)
		if relErr != nil {
			err = relErr
			return
		}
		out[entry.Path] = remoteWorkspaceFile{
			BaseSnapshotFile: BaseSnapshotFile{
				Path:        entry.Path,
				RelPath:     rel,
				ContentHash: entry.ContentHash,
				Mode:        normalizedSnapshotMode(entry.Mode),
				Size:        entry.Size,
			},
		}
	})
	return out, err
}
