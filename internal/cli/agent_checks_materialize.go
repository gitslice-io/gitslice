package cli

import (
	"context"

	"gitslice.io/gitslice/internal/agentd"
	"gitslice.io/gitslice/internal/clientcache"
)

type (
	RepoClient           = agentd.RepoClient
	checkMaterializePlan = agentd.CheckMaterializePlan
	checkTreeRepoClient  = agentd.CheckTreeRepoClient
)

var (
	checkTreeTarget                   = agentd.CheckTreeTarget
	checkTreeLocalPath                = agentd.CheckTreeLocalPath
	normalizeCheckMaterializePath     = agentd.NormalizeCheckMaterializePath
	normalizeCheckMaterializePrefixes = agentd.NormalizeCheckMaterializePrefixes
)

func (d *agentDaemon) materializeCheckTree(ctx context.Context, repo RepoClient, rootTreeID string, paths []string, destRoot string) error {
	cache, err := d.runner.objectCache()
	if err != nil {
		return err
	}
	fetchBatch := func(ctx context.Context, rf agentd.BatchReadClient, cache *clientcache.ObjectCache, rootTreeID string, targets []agentd.BatchReadTarget, workers int) error {
		readTargets := make([]remoteReadTarget, len(targets))
		for i, t := range targets {
			readTargets[i] = remoteReadTarget{Path: t.Path, ContentHash: t.ContentHash, Size: t.Size}
		}
		_, err := NewTransferManager(rf, nil, nil, cache).FetchFilesToCache(ctx, TransferFetchRequest{
			RootTreeID: rootTreeID,
			Targets:    readTargets,
			Workers:    workers,
		})
		return err
	}
	writer := agentd.CheckTreeWriter{
		EnsureDir: ensureConfinedDir,
		WriteFile: writeConfinedWorkspaceFile,
	}
	return agentd.MaterializeCheckTree(ctx, repo, cache, fetchBatch, writer, rootTreeID, paths, destRoot)
}
