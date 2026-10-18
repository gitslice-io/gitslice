package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// Moving a lazy workspace's layers (design/30_lazy_workspaces.md, "Moving the
// base"). gs sync, gs submit and the stack commands decide what a mounted
// workspace should show; its mount daemon, which owns the layers, applies
// that in one step and tells the kernel what changed. Paths nobody touched
// cost nothing: a new base is one listing, and a changeset's files become
// references, fetched only when something reads them.

// lazyWorkspace is a mounted lazy workspace, as a command sees it.
type lazyWorkspace struct {
	id       string
	root     string
	rec      mountRecord
	manifest *lazyfs.Manifest
}

// currentLazyWorkspace returns the lazy workspace the command runs in, or
// nil in a gs init workspace.
func (r Runner) currentLazyWorkspace() (*lazyWorkspace, error) {
	root, err := r.workspaceRoot()
	if err != nil {
		return nil, err
	}
	id := lazyWorkspaceID(root)
	if id == "" {
		return nil, nil
	}
	rec, err := r.readMountRecord(id)
	if err != nil {
		return nil, err
	}
	manifest, err := r.loadMountManifest(rec)
	if err != nil {
		return nil, err
	}
	return &lazyWorkspace{id: id, root: root, rec: rec, manifest: manifest}, nil
}

func (lw *lazyWorkspace) call(req mountControlRequest) (mountControlReply, error) {
	reply, err := callMountDaemon(lw.id, req)
	if errors.Is(err, errMountUnreachable) {
		return reply, userError("lazy_workspace_not_mounted", "cannot reach the mount of the lazy workspace at "+lw.root+": "+err.Error(), "Mount it again: gs mount "+lw.rec.Account+":"+lw.rec.Slice+" "+lw.root+".")
	}
	if err != nil && strings.Contains(err.Error(), lazyfs.ErrChanged.Error()) {
		return reply, userError("workspace_changed", "files in the workspace changed while gs was moving it: "+err.Error(), "Nothing was changed; run the command again.")
	}
	return reply, err
}

// rawEdits returns every change in the local layer, untracked and ignored
// files included.
func (lw *lazyWorkspace) rawEdits() ([]lazyfs.Edit, error) {
	reply, err := lw.call(mountControlRequest{Op: "edits"})
	if err != nil {
		return nil, err
	}
	if reply.BaseCommit != "" && reply.BaseCommit != lw.manifest.CommitID() {
		return nil, userError("workspace_changed", "the lazy workspace moved to another base meanwhile", "Run the command again.")
	}
	return reply.Edits, nil
}

// layerView reads the workspace's layers through its daemon.
func (lw *lazyWorkspace) layerView() daemonLayerView {
	return daemonLayerView{id: lw.id, manifest: lw.manifest, mountRoot: lw.root}
}

// listingAt returns the slice's listing at commitID, stored where the daemon
// reads it when moving the workspace there.
func (r Runner) lazyListingAt(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, lw *lazyWorkspace, commitID string) (*lazyfs.Manifest, error) {
	if commitID == lw.manifest.CommitID() {
		return lw.manifest, nil
	}
	cache, err := r.objectCache()
	if err != nil {
		return nil, err
	}
	checkout, err := receiveCheckoutListingAt(authContext(ctx, cfg), conn, lw.rec.ref(), commitID, cache)
	if err != nil {
		return nil, err
	}
	entries := manifestEntries(checkout.entries)
	manifest, err := lazyfs.NewManifest(checkout.commitID, entries)
	if err != nil {
		return nil, err
	}
	if err := writeManifest(r.mountManifestPath(lw.id, checkout.commitID), entries); err != nil {
		return nil, err
	}
	return manifest, nil
}

// manifestSnapshot is a manifest as a base snapshot, as .gs keeps it.
func manifestSnapshot(manifest *lazyfs.Manifest) BaseSnapshot {
	snapshot := BaseSnapshot{CommitID: manifest.CommitID(), Files: map[string]BaseSnapshotFile{}}
	manifest.Files(func(entry lazyfs.BaseEntry) {
		snapshot.Files[entry.Path] = BaseSnapshotFile{Path: entry.Path, RelPath: strings.TrimPrefix(entry.Path, "/"), ContentHash: entry.ContentHash, Mode: entry.Mode, Size: entry.Size}
	})
	return snapshot
}

// rebaseLazyWorkspace moves a lazy workspace onto newManifest, keeping its
// local changes. resolve decides every change the new base also made to the
// same path (lazyfs.NeedsMerge); the daemon refuses the rebase if a decided
// path changed again meanwhile.
func (r Runner) rebaseLazyWorkspace(lw *lazyWorkspace, newManifest *lazyfs.Manifest, resolve func(edit lazyfs.Edit) mountResolution) (mountControlReply, error) {
	raw, err := lw.rawEdits()
	if err != nil {
		return mountControlReply{}, err
	}
	resolutions := map[string]mountResolution{}
	for _, edit := range raw {
		if !lazyfs.NeedsMerge(edit, lw.manifest, newManifest) {
			continue
		}
		res := resolve(edit)
		res.Ours = editFingerprint(edit)
		resolutions[edit.Path] = res
	}
	reply, err := lw.call(mountControlRequest{Op: "rebase", Commit: newManifest.CommitID(), Resolutions: resolutions})
	if err != nil {
		return reply, err
	}
	lw.manifest = newManifest
	lw.rec.BaseCommit = newManifest.CommitID()
	return reply, nil
}

// switchLazyWorkspace makes a lazy workspace show snapshot, every file of a
// changeset as gs switch writes them into a gs init workspace, over
// newManifest, or over the workspace's base when newManifest is nil. Untracked
// files that .gitignore covers stay; every other local change is replaced, so
// the caller checks first that there is none it would lose. Nothing is
// fetched but symlink targets, and the sizes of files neither the cache nor
// the base knows.
func (r Runner) switchLazyWorkspace(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, lw *lazyWorkspace, newManifest *lazyfs.Manifest, snapshot BaseSnapshot) error {
	manifest := lw.manifest
	commit := ""
	if newManifest != nil {
		manifest, commit = newManifest, newManifest.CommitID()
	}
	raw, err := lw.rawEdits()
	if err != nil {
		return err
	}
	// The local layer's changes a walk would report, against its own base;
	// the rest of its changes are ignored files, which a switch keeps.
	source := overlayEditSource{fs: lw.layerView(), ws: ws, mountRoot: lw.root}
	reported, err := source.edits(ctx, manifestSnapshot(lw.manifest), 0)
	if err != nil {
		return err
	}
	reportedPaths := map[string]bool{}
	for _, edit := range reported.edits {
		reportedPaths[edit.Path] = true
	}
	var keep []string
	for _, edit := range raw {
		if _, tracked := snapshot.Files[edit.Path]; edit.Op != "delete" && !tracked && !reportedPaths[edit.Path] {
			keep = append(keep, edit.Path)
		}
	}
	diff, err := r.lazySnapshotDiff(ctx, conn, cfg, ws, manifest, snapshot)
	if err != nil {
		return err
	}
	if _, err := lw.call(mountControlRequest{Op: "switch", Commit: commit, Diff: diff, Keep: keep, Expect: raw}); err != nil {
		return err
	}
	lw.manifest = manifest
	lw.rec.BaseCommit = manifest.CommitID()
	return nil
}

// lazySnapshotDiff returns where snapshot differs from manifest, as
// lazyfs.FS.Switch takes it: files and symlinks the snapshot has differently,
// and nil for the manifest's files it does not have.
func (r Runner) lazySnapshotDiff(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, manifest *lazyfs.Manifest, snapshot BaseSnapshot) (map[string]*lazyfs.BaseEntry, error) {
	cache, err := r.objectCache()
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	manifest.Files(func(entry lazyfs.BaseEntry) {
		sizes[entry.ContentHash] = entry.Size
	})
	diff := map[string]*lazyfs.BaseEntry{}
	var unknown []string
	sliceRef := &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
	blob := corev1.NewBlobServiceClient(conn)
	for p, file := range snapshot.Files {
		entry, inBase := manifest.Lookup(p)
		if isSymlinkMode(file.Mode) {
			if inBase && entry.Kind == lazyfs.KindSymlink && entry.ContentHash == file.ContentHash {
				continue
			}
			target, err := readSnapshotContent(authContext(ctx, cfg), blob, cache, sliceRef, file.ContentHash)
			if err != nil {
				return nil, fmt.Errorf("symlink %s: %w", p, err)
			}
			diff[p] = &lazyfs.BaseEntry{Path: p, Kind: lazyfs.KindSymlink, Mode: lazyfs.ModeSymlink, Size: int64(len(target)), ContentHash: file.ContentHash, Target: string(target)}
			continue
		}
		if inBase && entry.Kind == lazyfs.KindFile && entry.ContentHash == file.ContentHash && sameWorkspaceMode(entry.Mode, file.Mode) {
			continue
		}
		diff[p] = &lazyfs.BaseEntry{Path: p, Kind: lazyfs.KindFile, Mode: normalizedSnapshotMode(file.Mode), ContentHash: file.ContentHash}
		if _, known := sizes[file.ContentHash]; !known {
			if path, err := cache.Path(file.ContentHash); err == nil && cache.Exists(file.ContentHash) {
				if info, err := os.Stat(path); err == nil {
					sizes[file.ContentHash] = info.Size()
					continue
				}
			}
			sizes[file.ContentHash] = -1
			unknown = append(unknown, file.ContentHash)
		}
	}
	manifest.Files(func(entry lazyfs.BaseEntry) {
		if _, ok := snapshot.Files[entry.Path]; !ok {
			diff[entry.Path] = nil
		}
	})
	if len(unknown) > 0 {
		sort.Strings(unknown)
		const batchSize = 512
		for start := 0; start < len(unknown); start += batchSize {
			end := min(start+batchSize, len(unknown))
			res, err := blob.GetBlobStatus(authContext(ctx, cfg), &corev1.GetBlobStatusRequest{ContentHashes: unknown[start:end], Slice: sliceRef})
			if err != nil {
				return nil, err
			}
			for _, record := range res.GetBlobs() {
				sizes[record.GetContentHash()] = record.GetSize()
			}
		}
	}
	for p, entry := range diff {
		if entry == nil || entry.Kind != lazyfs.KindFile {
			continue
		}
		size := sizes[entry.ContentHash]
		if size < 0 {
			return nil, userError("content_unavailable", "the server has no content "+entry.ContentHash+" for "+p, "Run gs deps and gs cs show to check the changeset.")
		}
		entry.Size = size
	}
	return diff, nil
}

// readSnapshotContent reads content by its hash: from the cache, else from
// the server, keeping a copy.
func readSnapshotContent(ctx context.Context, blob corev1.BlobServiceClient, cache *clientcache.ObjectCache, sliceRef *corev1.SliceRef, hash string) ([]byte, error) {
	if cache.Exists(hash) {
		return cache.Read(hash)
	}
	data, err := readBlobStreamBytes(ctx, blob, sliceRef, hash, 0, 0)
	if err != nil {
		return nil, err
	}
	cached, err := cache.PutBytes(data)
	if err != nil {
		return nil, err
	}
	if cached.ContentHash != hash {
		return nil, fmt.Errorf("content hash mismatch: got %s, want %s", cached.ContentHash, hash)
	}
	return data, nil
}
