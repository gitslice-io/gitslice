package cli

import (
	"context"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// workspaceSyncDriver abstracts eager checkouts (gs init) vs lazy FUSE mounts
// (gs mount) during gs sync so runWorkspaceSync stays free of interleaved mode branches.
type workspaceSyncDriver interface {
	isLazy() bool
	workingFiles(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, base BaseSnapshot) (map[string]workingFile, error)
	remoteFiles(ctx, callCtx context.Context, conn *grpc.ClientConn, cfg UserConfig, repo corev1.RepositoryServiceClient, ws WorkspaceConfig, newBaseCommitID string) (map[string]remoteWorkspaceFile, error)
	backupUncommitted(localDirtyPaths []string) (string, error)
	applySyncPlan(ctx, callCtx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, ws WorkspaceConfig, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remoteFiles map[string]remoteWorkspaceFile, plan workspaceSyncPlan, hasActiveStack bool) error
	applyStackSnapshot(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, activeStack *corev1.ChangesetStack, state *WorkspaceState, newBaseCommitID string, remoteSnapshot BaseSnapshot) error
}

func (r Runner) newWorkspaceSyncDriver(root string) (workspaceSyncDriver, error) {
	lazy, err := r.currentLazyWorkspace()
	if err != nil {
		return nil, err
	}
	if lazy != nil {
		return &lazyWorkspaceSyncDriver{r: r, lazy: lazy}, nil
	}
	return &eagerWorkspaceSyncDriver{r: r, root: root}, nil
}

type eagerWorkspaceSyncDriver struct {
	r    Runner
	root string
}

func (d *eagerWorkspaceSyncDriver) isLazy() bool { return false }

func (d *eagerWorkspaceSyncDriver) workingFiles(_ context.Context, _ *grpc.ClientConn, _ UserConfig, ws WorkspaceConfig, base BaseSnapshot) (map[string]workingFile, error) {
	return d.r.scanWorkspaceFiles(ws, base.Files)
}

func (d *eagerWorkspaceSyncDriver) remoteFiles(_, callCtx context.Context, _ *grpc.ClientConn, _ UserConfig, repo corev1.RepositoryServiceClient, ws WorkspaceConfig, newBaseCommitID string) (map[string]remoteWorkspaceFile, error) {
	return d.r.remoteWorkspaceFiles(callCtx, repo, ws, newBaseCommitID)
}

func (d *eagerWorkspaceSyncDriver) backupUncommitted(localDirtyPaths []string) (string, error) {
	return d.r.backupUncommittedEdits(d.root, localDirtyPaths)
}

func (d *eagerWorkspaceSyncDriver) applySyncPlan(ctx, callCtx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, ws WorkspaceConfig, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remoteFiles map[string]remoteWorkspaceFile, plan workspaceSyncPlan, _ bool) error {
	return d.r.applyWorkspaceSyncPlan(ctx, callCtx, repo, cache, d.root, ws, oldBaseCommitID, newBaseCommitID, base, current, remoteFiles, plan)
}

func (d *eagerWorkspaceSyncDriver) applyStackSnapshot(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, activeStack *corev1.ChangesetStack, state *WorkspaceState, newBaseCommitID string, remoteSnapshot BaseSnapshot) error {
	if state.CurrentChangesetID == "" {
		return nil
	}
	if entry := stackEntryByChangesetSelector(activeStack, state.CurrentChangesetID); entry != nil && entry.Changeset != nil {
		state.CurrentPatchsetID = entry.Changeset.CurrentPatchsetId
	}
	activeStack.BaseCommitId = newBaseCommitID
	targetSnapshot, err := d.r.stackSnapshotThroughChangesetFromBase(ctx, conn, cfg, ws, remoteSnapshot, activeStack, state.CurrentChangesetID)
	if err != nil {
		return err
	}
	return d.r.materializeWorkspaceSnapshot(ctx, conn, cfg, ws, targetSnapshot)
}

type lazyWorkspaceSyncDriver struct {
	r           Runner
	lazy        *lazyWorkspace
	newManifest *lazyfs.Manifest
}

func (d *lazyWorkspaceSyncDriver) isLazy() bool { return true }

func (d *lazyWorkspaceSyncDriver) workingFiles(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, base BaseSnapshot) (map[string]workingFile, error) {
	return d.r.lazyWorkingFiles(ctx, conn, cfg, ws, base)
}

func (d *lazyWorkspaceSyncDriver) remoteFiles(ctx, _ context.Context, conn *grpc.ClientConn, cfg UserConfig, _ corev1.RepositoryServiceClient, ws WorkspaceConfig, newBaseCommitID string) (map[string]remoteWorkspaceFile, error) {
	manifest, err := d.r.lazyListingAt(ctx, conn, cfg, d.lazy, newBaseCommitID)
	if err != nil {
		return nil, err
	}
	d.newManifest = manifest
	return manifestRemoteFiles(ws, manifest)
}

func (d *lazyWorkspaceSyncDriver) backupUncommitted(_ []string) (string, error) {
	return "", nil
}

func (d *lazyWorkspaceSyncDriver) applySyncPlan(_, callCtx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, _ WorkspaceConfig, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remoteFiles map[string]remoteWorkspaceFile, plan workspaceSyncPlan, hasActiveStack bool) error {
	if hasActiveStack {
		// With a dependency tree, the restack moves the workspace.
		return nil
	}
	return d.r.syncLazyWorkspace(callCtx, repo, cache, d.lazy, d.newManifest, oldBaseCommitID, newBaseCommitID, base, current, remoteFiles, plan)
}

func (d *lazyWorkspaceSyncDriver) applyStackSnapshot(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, activeStack *corev1.ChangesetStack, state *WorkspaceState, newBaseCommitID string, remoteSnapshot BaseSnapshot) error {
	if state.CurrentChangesetID != "" {
		if entry := stackEntryByChangesetSelector(activeStack, state.CurrentChangesetID); entry != nil && entry.Changeset != nil {
			state.CurrentPatchsetID = entry.Changeset.CurrentPatchsetId
		}
		activeStack.BaseCommitId = newBaseCommitID
		targetSnapshot, err := d.r.stackSnapshotThroughChangesetFromBase(ctx, conn, cfg, ws, remoteSnapshot, activeStack, state.CurrentChangesetID)
		if err != nil {
			return err
		}
		return d.r.switchLazyWorkspace(ctx, conn, cfg, ws, d.lazy, d.newManifest, targetSnapshot)
	}
	return d.r.switchLazyWorkspace(ctx, conn, cfg, ws, d.lazy, d.newManifest, remoteSnapshot)
}

// applyWorkspaceSyncPlan writes a sync's result into a gs init workspace.
func (r Runner) applyWorkspaceSyncPlan(ctx, callCtx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, root string, ws WorkspaceConfig, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remoteFiles map[string]remoteWorkspaceFile, plan workspaceSyncPlan) error {
	// runWorkspaceSync already prefetched this plan's blobs into the cache;
	// prefetching again would only re-hash every cached blob on disk.
	progress := startProgress(ctx, "syncing", "paths", len(plan.RemoteDeletes)+len(plan.RemoteUpserts)+len(plan.MergedFiles)+len(plan.Conflicts))
	defer progress.Finish()
	for _, p := range plan.RemoteDeletes {
		if err := removeWorkspaceFile(root, ws, current, p); err != nil {
			return err
		}
		progress.Add(1)
	}
	for _, file := range plan.RemoteUpserts {
		if err := r.writeWorkspaceRemoteFile(callCtx, repo, cache, root, ws, current, newBaseCommitID, file); err != nil {
			return err
		}
		progress.Add(1)
	}
	for _, file := range plan.MergedFiles {
		if err := r.writeWorkspaceMergedFile(root, ws, current, file); err != nil {
			return err
		}
		progress.Add(1)
	}
	for _, conflict := range plan.Conflicts {
		if err := r.materializeWorkspaceConflict(callCtx, repo, cache, root, ws, oldBaseCommitID, newBaseCommitID, base, current, remoteFiles, conflict); err != nil {
			return err
		}
		progress.Add(1)
	}
	return nil
}

// lazyWorkingFiles is a lazy workspace's files as a walk would find them:
// its base with its local layer's changes applied. Changed files are read
// through the mount; nothing else is touched.
func (r Runner) lazyWorkingFiles(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, base BaseSnapshot) (map[string]workingFile, error) {
	edits, files, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, base, false)
	if err != nil {
		return nil, err
	}
	current := workingFilesFromSnapshot(base)
	for _, edit := range edits {
		if edit.Op == "delete" {
			delete(current, edit.Path)
			continue
		}
		current[edit.Path] = files[edit.Path]
	}
	return current, nil
}

// syncLazyWorkspace moves a lazy workspace with no dependency tree onto the
// new base, resolving what both sides changed as plan decided: merged files
// and conflict markers go to the local layer, "theirs" takes the new base,
// and everything else keeps the local change.
func (r Runner) syncLazyWorkspace(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, lazy *lazyWorkspace, newManifest *lazyfs.Manifest, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remoteFiles map[string]remoteWorkspaceFile, plan workspaceSyncPlan) error {
	merged := map[string]mergedWorkspaceFile{}
	for _, file := range plan.MergedFiles {
		merged[file.Path] = file
	}
	conflicts := map[string]mountResolution{}
	for _, conflict := range plan.Conflicts {
		data, mode, ok, err := r.workspaceConflictContent(ctx, repo, cache, oldBaseCommitID, newBaseCommitID, base, current, remoteFiles, conflict)
		if err != nil {
			return err
		}
		if ok {
			conflicts[conflict.Path] = mountResolution{Content: data, Mode: mode, Conflict: true}
		} else {
			conflicts[conflict.Path] = mountResolution{Keep: true, Conflict: true}
		}
	}
	updated := map[string]bool{}
	for _, p := range plan.UpdatedPaths {
		updated[p] = true
	}
	_, err := r.rebaseLazyWorkspace(lazy, newManifest, func(edit lazyfs.Edit) mountResolution {
		if file, ok := merged[edit.Path]; ok {
			return mountResolution{Content: file.Data, Mode: normalizedSnapshotMode(file.Mode)}
		}
		if res, ok := conflicts[edit.Path]; ok {
			return res
		}
		if updated[edit.Path] {
			return mountResolution{Theirs: true}
		}
		return mountResolution{Keep: true}
	})
	return err
}
