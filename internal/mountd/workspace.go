//go:build linux || darwin

package mountd

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
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// Env provides the runtime hooks and paths needed to attach and supervise
// mounted lazy workspaces.
type Env struct {
	Home                   string
	MountsRoot             string
	RuntimeDir             string
	Insecure               bool
	ObjectCache            func() (*clientcache.ObjectCache, error)
	PruneObjectCache       func()
	DialLazy               func(serverAddr string, insecure bool) (*grpc.ClientConn, error)
	FetchFiles             func(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error)
	WriteLazyWorkspaceInit func(rec Record, manifest *lazyfs.Manifest) error
}

// AttachedWorkspace is a lazy workspace served over FUSE by this process.
type AttachedWorkspace struct {
	env       Env
	ctx       context.Context
	rec       Record
	cache     *clientcache.ObjectCache
	conn      *grpc.ClientConn
	fetcher   *Fetcher
	layer     *lazyfs.FS
	mounted   *fusefs.Mounted
	mountedAt time.Time
	logf      func(format string, args ...any)

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

// MountTimeoutHint returns platform-specific guidance when a FUSE mount times out.
func MountTimeoutHint(goos string) string {
	if goos == "darwin" {
		return "macFUSE is probably installed but its system extension is not approved yet. " +
			"Open System Settings > Privacy & Security, scroll to the message about blocked system software, " +
			"click Allow for macFUSE, and restart your Mac (on Apple silicon you may first need to allow " +
			"third-party extensions in Recovery mode: Startup Security Utility > Reduced Security). " +
			"Then run gs doctor to check it works. Until then, gs init (without --lazy) checks the slice out as plain files."
	}
	return "The kernel did not answer the FUSE mount. Check that /dev/fuse exists, fusermount3 is installed (package fuse3) and you may use it, then try again. Without FUSE, use gs init."
}

// AttachWorkspace opens the workspace's layers and mounts them at its mount path.
func (e Env) AttachWorkspace(ctx context.Context, rec Record, debug bool, logf func(format string, args ...any)) (*AttachedWorkspace, error) {
	if err := ValidateID(rec.ID); err != nil {
		return nil, err
	}
	state := StateDir(e.MountsRoot, rec.ID)
	if err := verifyRealWorkspaceDir(state); err != nil {
		return nil, err
	}
	local := filepath.Join(state, "local")
	if err := verifyRealWorkspaceDir(local); err != nil {
		return nil, err
	}
	if recorded, err := lazyfs.RecordedBase(local); err == nil && recorded != "" && recorded != rec.BaseCommit {
		if _, err := os.Stat(ManifestPath(e.MountsRoot, rec.ID, recorded)); err == nil {
			rec.BaseCommit = recorded
		}
	}
	manifest, err := LoadManifest(e.MountsRoot, rec)
	if err != nil {
		return nil, err
	}
	rec.Files = ManifestFileCount(manifest)
	cache, err := e.ObjectCache()
	if err != nil {
		return nil, err
	}
	conn, err := e.DialLazy(rec.ServerAddr, e.Insecure)
	if err != nil {
		return nil, err
	}
	sliceRef := rec.Ref()
	fetcher := NewFetcher(conn, sliceRef, cache, manifest, func(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error) {
		return e.FetchFiles(ctx, conn, sliceRef, commitID, entries, cache)
	})
	layer, err := lazyfs.Open(local, manifest, Content{CacheContent: CacheContent{Cache: cache}, Fetcher: fetcher}, lazyfs.Options{NoSync: true})
	if err != nil {
		conn.Close()
		return nil, err
	}
	w := &AttachedWorkspace{
		env:              e,
		ctx:              ctx,
		rec:              rec,
		cache:            cache,
		conn:             conn,
		fetcher:          fetcher,
		layer:            layer,
		logf:             logf,
		cachedBaseDigest: ManifestDigest(manifest),
	}
	gsDir := GsDir(e.MountsRoot, rec.ID)
	if err := verifyRealWorkspaceDir(gsDir); err != nil {
		w.Close()
		return nil, err
	}
	if e.WriteLazyWorkspaceInit != nil {
		if err := e.WriteLazyWorkspaceInit(rec, manifest); err != nil {
			w.Close()
			return nil, err
		}
	}
	if err := verifyRealWorkspaceDir(gsDir); err != nil {
		w.Close()
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
		w.Close()
		if errors.Is(err, fusefs.ErrMountTimeout) {
			return nil, NewError("mount_timeout", "mounting "+rec.MountPath+" did not finish in "+fusefs.DefaultMountTimeout.String(), MountTimeoutHint(runtime.GOOS))
		}
		return nil, NewError("mount_failed", "cannot mount "+rec.MountPath+": "+err.Error(), "gs mount needs FUSE: on Linux /dev/fuse and fusermount3 (package fuse3), on macOS macFUSE. Without it, use gs init.")
	}
	w.mounted = mounted
	w.mountedAt = time.Now().UTC()
	w.rec.MountedAt = w.mountedAt.Format(time.RFC3339)
	_ = WriteRecord(e.MountsRoot, w.rec)
	w.prefetch(manifest)
	return w, nil
}

func (w *AttachedWorkspace) Unmount() { _ = w.mounted.Server.Unmount() }

func (w *AttachedWorkspace) Wait() { w.mounted.Server.Wait() }

func (w *AttachedWorkspace) Close() {
	if w.layer != nil {
		_ = w.layer.Close()
	}
	if w.conn != nil {
		_ = w.conn.Close()
	}
}

func (w *AttachedWorkspace) prefetch(manifest *lazyfs.Manifest) {
	if len(w.rec.Prefetch) == 0 || w.env.FetchFiles == nil {
		return
	}
	go func() {
		entries := PrefetchEntries(manifest, w.rec.Prefetch, w.cache)
		if len(entries) == 0 {
			return
		}
		started := time.Now()
		sliceRef := w.rec.Ref()
		files, bytes, err := FetchConcurrently(w.ctx, entries, func(ctx context.Context, group []lazyfs.BaseEntry) (int, int64, error) {
			return w.env.FetchFiles(ctx, w.conn, sliceRef, manifest.CommitID(), group, w.cache)
		})
		w.fetcher.AddStats(files, bytes)
		if err != nil {
			w.logf("prefetch %s: %d of %d files fetched: %v", w.rec.ID, files, len(entries), err)
			return
		}
		w.logf("prefetch %s: %d files (%d B) in %s", w.rec.ID, files, bytes, time.Since(started).Round(time.Millisecond))
	}()
}

func (w *AttachedWorkspace) moveLayers(req ControlRequest, reply *ControlReply) error {
	w.moveMu.Lock()
	defer w.moveMu.Unlock()
	newBase := w.layer.Base()
	if req.Commit != "" && req.Commit != newBase.CommitID() {
		m, err := ReadManifest(ManifestPath(w.env.MountsRoot, w.rec.ID, req.Commit), req.Commit)
		if err != nil {
			return fmt.Errorf("no listing of %s for this workspace: %w", req.Commit, err)
		}
		newBase = m
	}
	var changed []string
	if req.Op == "rebase" {
		result, err := w.layer.Rebase(context.Background(), newBase, func(ctx context.Context, c lazyfs.MergeCase) (lazyfs.MergeResult, error) {
			res, ok := req.Resolutions[c.Path]
			if !ok || res.Ours != EditFingerprint(*c.Ours) {
				return lazyfs.MergeResult{}, fmt.Errorf("%w: %s", lazyfs.ErrChanged, c.Path)
			}
			return res.Result(), nil
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
	w.fetcher.SetBase(newBase)
	w.setBaseDigest(ManifestDigest(newBase))
	if newBase.CommitID() != w.rec.BaseCommit {
		previous := w.rec.BaseCommit
		w.rec.BaseCommit, w.rec.Files = newBase.CommitID(), ManifestFileCount(newBase)
		if err := WriteRecord(w.env.MountsRoot, w.rec); err != nil {
			return err
		}
		RemoveStaleManifests(w.env.MountsRoot, w.rec.ID, w.rec.BaseCommit, previous)
	}
	w.mounted.InvalidatePaths(changed)
	w.prefetch(newBase)
	reply.Changed = len(changed)
	reply.BaseCommit = newBase.CommitID()
	return nil
}

func (w *AttachedWorkspace) baseDigest() string {
	w.digestMu.RLock()
	defer w.digestMu.RUnlock()
	return w.cachedBaseDigest
}

func (w *AttachedWorkspace) setBaseDigest(digest string) {
	w.digestMu.Lock()
	w.cachedBaseDigest = digest
	w.digestMu.Unlock()
}

// PopulateReplyBaseFiles fills reply.BaseFilesDigest and reply.BaseFiles for edited paths.
func PopulateReplyBaseFiles(reply *ControlReply, manifest *lazyfs.Manifest, digest string, edits []lazyfs.Edit) {
	reply.BaseFilesDigest = digest
	if manifest == nil || len(edits) == 0 {
		return
	}
	baseFiles := make(map[string]BaseFile, len(edits))
	for _, edit := range edits {
		if entry, ok := manifest.Lookup(edit.Path); ok && entry.Kind != lazyfs.KindDir {
			baseFiles[edit.Path] = BaseFile{
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

func (w *AttachedWorkspace) Handle(req ControlRequest) (reply ControlReply) {
	defer func() {
		if p := recover(); p != nil {
			reply.Error = fmt.Sprintf("internal error serving %s: %v", req.Op, p)
		}
	}()
	stats := w.mounted.Stats()
	files, bytes := w.fetcher.Stats()
	base := w.layer.Base()
	reply = ControlReply{PID: os.Getpid(), MountedAt: w.mountedAt.Format(time.RFC3339), Opens: stats.Opens, Fetches: stats.Fetches, FetchedFiles: files, FetchedBytes: bytes, Errors: stats.Errors, LastError: stats.LastError, BaseCommit: base.CommitID()}
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
		edits, err := w.layer.Edits()
		if err != nil {
			reply.Error = err.Error()
			break
		}
		reply.Edits = edits
		PopulateReplyBaseFiles(&reply, w.layer.Base(), w.baseDigest(), edits)
	case "restore":
		before, _ := w.layer.Edits()
		for _, p := range req.Paths {
			if err := w.layer.Restore(p); err != nil {
				reply.Error = err.Error()
				break
			}
			w.mounted.Invalidate(p)
		}
		if after, _ := w.layer.Edits(); len(before) > len(after) {
			reply.Changed = len(before) - len(after)
		}
	case "unmount":
		go w.Unmount()
	}
	return reply
}
