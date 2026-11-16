package cli

import (
	"context"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/mergerules"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// applySyncMergeRule resolves a sync conflict by the path's rule in
// .gitslice/merge.yaml, read from the new base: union merges the lines,
// newest takes the new base's version, ours keeps the workspace's. It
// reports whether a rule resolved the conflict.
func (r Runner) applySyncMergeRule(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, conflict WorkspaceConflict, plan *workspaceSyncPlan) (bool, error) {
	strategy, ok, err := mergerules.Lookup(conflict.Path, func(cfg string) ([]byte, bool, error) {
		file, ok := remote[cfg]
		if !ok {
			return nil, false, nil
		}
		return r.remoteFileBytes(ctx, repo, cache, newBaseCommitID, file.BaseSnapshotFile, true)
	})
	if err != nil || !ok {
		return false, err
	}
	remoteFile, remoteOK := remote[conflict.Path]
	switch strategy {
	case mergerules.Ours:
		return true, nil
	case mergerules.Newest:
		if remoteOK {
			plan.RemoteUpserts = append(plan.RemoteUpserts, remoteFile)
		} else {
			plan.RemoteDeletes = append(plan.RemoteDeletes, conflict.Path)
		}
		plan.UpdatedPaths = appendUniqueString(plan.UpdatedPaths, conflict.Path)
		plan.LocalPaths = removeStringValue(plan.LocalPaths, conflict.Path)
		return true, nil
	case mergerules.Union:
		baseFile, baseOK := base.Files[conflict.Path]
		localFile, localOK := current[conflict.Path]
		if !baseOK || !localOK || !remoteOK {
			return false, nil
		}
		baseData, baseExists, err := r.remoteFileBytes(ctx, repo, cache, oldBaseCommitID, baseFile, true)
		if err != nil {
			return false, err
		}
		localData, localExists, err := localWorkspaceFileBytes(localFile, true)
		if err != nil {
			return false, err
		}
		remoteData, remoteExists, err := r.remoteFileBytes(ctx, repo, cache, newBaseCommitID, remoteFile.BaseSnapshotFile, true)
		if err != nil {
			return false, err
		}
		if !baseExists || !localExists || !remoteExists {
			return false, nil
		}
		plan.MergedFiles = append(plan.MergedFiles, mergedWorkspaceFile{
			Path: conflict.Path,
			Data: mergerules.MergeUnion(baseData, localData, remoteData),
			Mode: normalizedSnapshotMode(localFile.Mode),
		})
		plan.MergedPaths = appendUniqueString(plan.MergedPaths, conflict.Path)
		return true, nil
	}
	return false, nil
}
