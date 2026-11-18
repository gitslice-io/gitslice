//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	"gitslice.io/gitslice/internal/lazyfs/fusefs"
	"google.golang.org/grpc"
)

// attachedWorkspace is a lazy workspace served over FUSE by this process: its
// layers, its fetcher, and the mount. A mount process serves one (gs mount
// --foreground) or many (the mount supervisor, mountd.go).
type attachedWorkspace struct {
	r         Runner
	ctx       context.Context
	rec       mountRecord
	cache     *clientcache.ObjectCache
	conn      *grpc.ClientConn
	fetcher   *mountFetcher
	layer     *lazyfs.FS
	mounted   *fusefs.Mounted
	mountedAt time.Time
	logf      func(format string, args ...any)

	// moveMu serializes rebase and switch: each replaces the whole local layer.
	moveMu sync.Mutex

	digestMu         sync.RWMutex
	cachedBaseDigest string
}

func verifyRealWorkspaceDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", dir)
	}
	return nil
}

// attachWorkspace opens the workspace's layers and mounts them at its mount
// path. It does not wait for the server: only a read of content the cache
// lacks needs it, so a workspace can be mounted offline.
func (r Runner) attachWorkspace(ctx context.Context, rec mountRecord, debug bool, logf func(format string, args ...any)) (*attachedWorkspace, error) {
	if err := validateMountID(rec.ID); err != nil {
		return nil, err
	}
	state := r.mountStateDir(rec.ID)
	if err := verifyRealWorkspaceDir(state); err != nil {
		return nil, err
	}
	local := filepath.Join(state, "local")
	if err := verifyRealWorkspaceDir(local); err != nil {
		return nil, err
	}
	// A base move that reached the layers but not mount.json (the mount stopped
	// in between) finishes here: the layers name their base.
	if recorded, err := lazyfs.RecordedBase(local); err == nil && recorded != "" && recorded != rec.BaseCommit {
		if _, err := os.Stat(r.mountManifestPath(rec.ID, recorded)); err == nil {
			rec.BaseCommit = recorded
		}
	}
	manifest, err := r.loadMountManifest(rec)
	if err != nil {
		return nil, err
	}
	rec.Files = manifestFileCount(manifest)
	cache, err := r.objectCache()
	if err != nil {
		return nil, err
	}
	conn, err := dialLazy(rec.ServerAddr, r.Insecure)
	if err != nil {
		return nil, err
	}
	fetcher := newMountFetcher(r, conn, rec.ref(), cache, manifest)
	layer, err := lazyfs.Open(local, manifest, mountContent{cacheContent: cacheContent{cache: cache}, fetcher: fetcher}, lazyfs.Options{NoSync: true})
	if err != nil {
		conn.Close()
		return nil, err
	}
	w := &attachedWorkspace{
		r:                r,
		ctx:              ctx,
		rec:              rec,
		cache:            cache,
		conn:             conn,
		fetcher:          fetcher,
		layer:            layer,
		logf:             logf,
		cachedBaseDigest: manifestDigest(manifest),
	}
	gsDir := r.mountGsDir(rec.ID)
	if err := verifyRealWorkspaceDir(gsDir); err != nil {
		w.close()
		return nil, err
	}
	if err := r.writeLazyWorkspaceState(rec, manifest); err != nil {
		w.close()
		return nil, err
	}
	if err := verifyRealWorkspaceDir(gsDir); err != nil {
		w.close()
		return nil, err
	}
	mounted, err := fusefs.Mount(rec.MountPath, layer, fetcher, fusefs.Options{
		FsName:   "gitslice:" + rec.Account + "/" + rec.Slice,
		UID:      uint32(os.Getuid()),
		GID:      uint32(os.Getgid()),
		Debug:    debug,
		ReadOnly: rec.ReadOnly,
		GsDir:    gsDir,
	})
	if err != nil {
		w.close()
		if errors.Is(err, fusefs.ErrMountTimeout) {
			return nil, userError("mount_timeout", "mounting "+rec.MountPath+" did not finish in "+fusefs.DefaultMountTimeout.String(), mountTimeoutHint(runtime.GOOS))
		}
		return nil, userError("mount_failed", "cannot mount "+rec.MountPath+": "+err.Error(), "gs mount needs FUSE: on Linux /dev/fuse and fusermount3 (package fuse3), on macOS macFUSE. Without it, use gs init.")
	}
	w.mounted = mounted
	w.mountedAt = time.Now().UTC()
	w.rec.MountedAt = w.mountedAt.Format(time.RFC3339)
	_ = r.writeMountRecord(w.rec)
	w.prefetch(manifest)
	return w, nil
}

// unmount asks the kernel to unmount; wait returns once it has.
func (w *attachedWorkspace) unmount() { _ = w.mounted.Server.Unmount() }

func (w *attachedWorkspace) wait() { w.mounted.Server.Wait() }

// close releases the layers and the connection, after the mount is gone.
func (w *attachedWorkspace) close() {
	if w.layer != nil {
		_ = w.layer.Close()
	}
	if w.conn != nil {
		_ = w.conn.Close()
	}
}

// prefetch fetches the files the workspace's --prefetch patterns name, in the
// background.
func (w *attachedWorkspace) prefetch(manifest *lazyfs.Manifest) {
	if len(w.rec.Prefetch) == 0 {
		return
	}
	go func() {
		entries := prefetchEntries(manifest, w.rec.Prefetch, w.cache)
		if len(entries) == 0 {
			return
		}
		cfg, err := w.r.readUserConfig()
		if err != nil {
			return
		}
		started := time.Now()
		files, bytes, err := fetchConcurrently(authContext(w.ctx, cfg), w.conn, w.rec.ref(), manifest.CommitID(), entries, w.cache)
		w.fetcher.mu.Lock()
		w.fetcher.files += int64(files)
		w.fetcher.bytes += bytes
		w.fetcher.mu.Unlock()
		if err != nil {
			w.logf("prefetch %s: %d of %d files fetched: %v", w.rec.ID, files, len(entries), err)
			return
		}
		w.logf("prefetch %s: %d files (%s) in %s", w.rec.ID, files, humanBytes(bytes), time.Since(started).Round(time.Millisecond))
	}()
}

// moveLayers runs a rebase or a switch, one at a time, with mount.json and the
// fetcher following.
func (w *attachedWorkspace) moveLayers(req mountControlRequest, reply *mountControlReply) error {
	w.moveMu.Lock()
	defer w.moveMu.Unlock()
	newBase := w.layer.Base()
	if req.Commit != "" && req.Commit != newBase.CommitID() {
		m, err := readManifest(w.r.mountManifestPath(w.rec.ID, req.Commit), req.Commit)
		if err != nil {
			return fmt.Errorf("no listing of %s for this workspace: %w", req.Commit, err)
		}
		newBase = m
	}
	var changed []string
	if req.Op == "rebase" {
		result, err := w.layer.Rebase(context.Background(), newBase, func(ctx context.Context, c lazyfs.MergeCase) (lazyfs.MergeResult, error) {
			res, ok := req.Resolutions[c.Path]
			if !ok || res.Ours != editFingerprint(*c.Ours) {
				return lazyfs.MergeResult{}, fmt.Errorf("%w: %s", lazyfs.ErrChanged, c.Path)
			}
			return res.result(), nil
		})
		if err != nil {
			return err
		}
		reply.Conflicts, changed = result.Conflicts, result.Changed
	} else {
		var err error
		if changed, err = w.layer.Switch(newBase, req.Diff, req.Keep, req.Expect); err != nil {
			return err
		}
	}
	w.fetcher.setBase(newBase)
	w.setBaseDigest(manifestDigest(newBase))
	if newBase.CommitID() != w.rec.BaseCommit {
		previous := w.rec.BaseCommit
		w.rec.BaseCommit, w.rec.Files = newBase.CommitID(), manifestFileCount(newBase)
		if err := w.r.writeMountRecord(w.rec); err != nil {
			return err
		}
		// The previous listing stays one more move, for a command that read
		// mount.json just before.
		w.r.removeStaleManifests(w.rec.ID, w.rec.BaseCommit, previous)
	}
	w.mounted.InvalidatePaths(changed)
	w.prefetch(newBase)
	reply.Changed = len(changed)
	reply.BaseCommit = newBase.CommitID()
	return nil
}

func (w *attachedWorkspace) baseDigest() string {
	w.digestMu.RLock()
	defer w.digestMu.RUnlock()
	return w.cachedBaseDigest
}

func (w *attachedWorkspace) setBaseDigest(digest string) {
	w.digestMu.Lock()
	w.cachedBaseDigest = digest
	w.digestMu.Unlock()
}

// populateReplyBaseFiles fills reply.BaseFiles and reply.BaseFilesDigest from
// manifest for the touched paths in edits.
func populateReplyBaseFiles(reply *mountControlReply, manifest *lazyfs.Manifest, digest string, edits []lazyfs.Edit) {
	reply.BaseFilesDigest = digest
	if manifest == nil || len(edits) == 0 {
		return
	}
	baseFiles := make(map[string]BaseSnapshotFile, len(edits))
	for _, edit := range edits {
		if entry, ok := manifest.Lookup(edit.Path); ok && entry.Kind != lazyfs.KindDir {
			baseFiles[edit.Path] = BaseSnapshotFile{
				Path:        entry.Path,
				RelPath:     strings.TrimPrefix(entry.Path, "/"),
				ContentHash: entry.ContentHash,
				Mode:        entry.Mode,
				Size:        entry.Size,
			}
		}
	}
	if len(baseFiles) > 0 {
		reply.BaseFiles = baseFiles
	}
}

// handle answers one control request for this workspace.
func (w *attachedWorkspace) handle(req mountControlRequest) (reply mountControlReply) {
	// One workspace's failure must not take the others down with it.
	defer func() {
		if p := recover(); p != nil {
			reply.Error = fmt.Sprintf("internal error serving %s: %v", req.Op, p)
		}
	}()
	stats := w.mounted.Stats()
	w.fetcher.mu.Lock()
	files, bytes := w.fetcher.files, w.fetcher.bytes
	w.fetcher.mu.Unlock()
	base := w.layer.Base()
	reply = mountControlReply{PID: os.Getpid(), MountedAt: w.mountedAt.Format(time.RFC3339), Opens: stats.Opens, Fetches: stats.Fetches, FetchedFiles: files, FetchedBytes: bytes, Errors: stats.Errors, LastError: stats.LastError, BaseCommit: base.CommitID()}
	switch req.Op {
	case "rebase", "switch":
		if w.rec.ReadOnly {
			reply.Error = "the workspace is mounted read-only"
			break
		}
		if err := w.moveLayers(req, &reply); err != nil {
			reply.Error = err.Error()
		}
	case "edits":
		// Hashing a local file also puts it in the client cache, where gs
		// create uploads it from.
		edits, err := w.layer.Edits()
		if err != nil {
			reply.Error = err.Error()
			break
		}
		reply.Edits = edits
		populateReplyBaseFiles(&reply, w.layer.Base(), w.baseDigest(), edits)
	case "restore":
		before, _ := w.layer.Edits()
		for _, p := range req.Paths {
			if err := w.layer.Restore(p); err != nil {
				reply.Error = err.Error()
				break
			}
			w.mounted.Invalidate(p)
		}
		// Changed is how many edits the restore dropped.
		if after, _ := w.layer.Edits(); len(before) > len(after) {
			reply.Changed = len(before) - len(after)
		}
	case "unmount":
		go w.unmount()
	}
	return reply
}
