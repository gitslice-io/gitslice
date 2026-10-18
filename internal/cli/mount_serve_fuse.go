//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
	"gitslice.io/gitslice/internal/lazyfs/fusefs"
)

// serveMount serves a lazy workspace at its mount path until it is unmounted,
// asked to over the control socket, interrupted, or ctx ends.
func (r Runner) serveMount(ctx context.Context, rec mountRecord, debug bool, ready func()) error {
	state := r.mountStateDir(rec.ID)
	local := filepath.Join(state, "local")
	// A base move that reached the layers but not mount.json (the daemon
	// stopped in between) finishes here: the layers name their base.
	if recorded, err := lazyfs.RecordedBase(local); err == nil && recorded != "" && recorded != rec.BaseCommit {
		if _, err := os.Stat(r.mountManifestPath(rec.ID, recorded)); err == nil {
			rec.BaseCommit = recorded
		}
	}
	manifest, err := r.loadMountManifest(rec)
	if err != nil {
		return err
	}
	rec.Files = manifestFileCount(manifest)
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	// The mount works from local state, so it starts without the server: only
	// a read of content the cache lacks needs it.
	conn, err := dialLazy(rec.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	fetcher := newMountFetcher(r, conn, rec.ref(), cache, manifest)
	layer, err := lazyfs.Open(local, manifest, mountContent{cacheContent: cacheContent{cache: cache}, fetcher: fetcher}, lazyfs.Options{})
	if err != nil {
		return err
	}
	defer layer.Close()
	if err := r.writeLazyWorkspaceState(rec, manifest); err != nil {
		return err
	}
	// The control socket listens before the mount appears, so a command run
	// right after gs mount always finds it (connections wait until served).
	socket := mountControlPath(rec.ID)
	var listener net.Listener
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err == nil {
		_ = os.Remove(socket)
		if l, err := net.Listen("unix", socket); err == nil {
			listener = l
			defer os.Remove(socket)
			defer listener.Close()
		}
	}
	mounted, err := fusefs.Mount(rec.MountPath, layer, fetcher, fusefs.Options{
		FsName:   "gitslice:" + rec.Account + "/" + rec.Slice,
		UID:      uint32(os.Getuid()),
		GID:      uint32(os.Getgid()),
		Debug:    debug,
		ReadOnly: rec.ReadOnly,
		GsDir:    r.mountGsDir(rec.ID),
	})
	if err != nil {
		return userError("mount_failed", "cannot mount "+rec.MountPath+": "+err.Error(), "gs mount needs FUSE: on Linux /dev/fuse and fusermount3 (package fuse3), on macOS macFUSE. Without it, use gs init.")
	}
	mountedAt := time.Now().UTC()
	rec.MountedAt = mountedAt.Format(time.RFC3339)
	_ = r.writeMountRecord(rec)

	unmount := func() { _ = mounted.Server.Unmount() }
	// prefetch fetches the files the workspace's --prefetch patterns name,
	// in the background.
	prefetch := func(manifest *lazyfs.Manifest) {
		if len(rec.Prefetch) == 0 {
			return
		}
		go func() {
			entries := prefetchEntries(manifest, rec.Prefetch, cache)
			if len(entries) == 0 {
				return
			}
			cfg, err := r.readUserConfig()
			if err != nil {
				return
			}
			started := time.Now()
			files, bytes, err := fetchConcurrently(authContext(ctx, cfg), conn, rec.ref(), manifest.CommitID(), entries, cache)
			fetcher.mu.Lock()
			fetcher.files += int64(files)
			fetcher.bytes += bytes
			fetcher.mu.Unlock()
			if err != nil {
				fmt.Fprintf(os.Stderr, "prefetch: %d of %d files fetched: %v\n", files, len(entries), err)
				return
			}
			fmt.Fprintf(os.Stderr, "prefetch: %d files (%s) in %s\n", files, humanBytes(bytes), time.Since(started).Round(time.Millisecond))
		}()
	}
	// moveLayers runs a rebase or a switch: one at a time, as each replaces
	// the whole local layer, and with mount.json and the fetcher following.
	var moveMu sync.Mutex
	moveLayers := func(req mountControlRequest, reply *mountControlReply) error {
		moveMu.Lock()
		defer moveMu.Unlock()
		newBase := layer.Base()
		if req.Commit != "" && req.Commit != newBase.CommitID() {
			m, err := readManifest(r.mountManifestPath(rec.ID, req.Commit), req.Commit)
			if err != nil {
				return fmt.Errorf("no listing of %s for this workspace: %w", req.Commit, err)
			}
			newBase = m
		}
		var changed []string
		if req.Op == "rebase" {
			result, err := layer.Rebase(context.Background(), newBase, func(ctx context.Context, c lazyfs.MergeCase) (lazyfs.MergeResult, error) {
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
			if changed, err = layer.Switch(newBase, req.Diff, req.Keep, req.Expect); err != nil {
				return err
			}
		}
		fetcher.setBase(newBase)
		if newBase.CommitID() != rec.BaseCommit {
			previous := rec.BaseCommit
			rec.BaseCommit, rec.Files = newBase.CommitID(), manifestFileCount(newBase)
			if err := r.writeMountRecord(rec); err != nil {
				return err
			}
			// The previous listing stays one more move, for a command that
			// read mount.json just before.
			r.removeStaleManifests(rec.ID, rec.BaseCommit, previous)
		}
		mounted.InvalidatePaths(changed)
		prefetch(newBase)
		reply.Changed = len(changed)
		reply.BaseCommit = newBase.CommitID()
		return nil
	}
	if listener != nil {
		{
			go serveMountControl(listener, func(req mountControlRequest) mountControlReply {
				stats := mounted.Stats()
				fetcher.mu.Lock()
				files, bytes := fetcher.files, fetcher.bytes
				fetcher.mu.Unlock()
				reply := mountControlReply{PID: os.Getpid(), MountedAt: mountedAt.Format(time.RFC3339), Opens: stats.Opens, Fetches: stats.Fetches, FetchedFiles: files, FetchedBytes: bytes, Errors: stats.Errors, LastError: stats.LastError, BaseCommit: layer.Base().CommitID()}
				switch req.Op {
				case "rebase", "switch":
					if rec.ReadOnly {
						reply.Error = "the workspace is mounted read-only"
						break
					}
					if err := moveLayers(req, &reply); err != nil {
						reply.Error = err.Error()
					}
				case "edits":
					// Hashing a local file also puts it in the client cache,
					// where gs create uploads it from.
					edits, err := layer.Edits()
					if err != nil {
						reply.Error = err.Error()
					}
					reply.Edits = edits
				case "restore":
					before, _ := layer.Edits()
					for _, p := range req.Paths {
						if err := layer.Restore(p); err != nil {
							reply.Error = err.Error()
							break
						}
						mounted.Invalidate(p)
					}
					// Changed is how many edits the restore dropped.
					if after, _ := layer.Edits(); len(before) > len(after) {
						reply.Changed = len(before) - len(after)
					}
				case "unmount":
					go unmount()
				}
				return reply
			})
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-signals:
			unmount()
		case <-ctx.Done():
			unmount()
		case <-stop:
		}
	}()
	prefetch(manifest)
	if ready != nil {
		ready()
	}
	mounted.Server.Wait()
	return nil
}

func serveMountControl(listener net.Listener, handle func(mountControlRequest) mountControlReply) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
			var req mountControlRequest
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				return
			}
			_ = json.NewEncoder(conn).Encode(handle(req))
		}(conn)
	}
}

// startMountDaemon serves the mount from a detached copy of gs, and waits
// until the mount is up.
func (r Runner) startMountDaemon(rec mountRecord) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(r.mountStateDir(rec.ID), "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "mount", "--foreground", "--workspace", rec.ID, rec.MountPath)
	cmd.Env = os.Environ()
	if r.Home != "" {
		cmd.Env = append(cmd.Env, "HOME="+r.Home)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(20 * time.Second)
	for {
		if mountIsUp(rec.MountPath) {
			_ = cmd.Process.Release()
			return nil
		}
		select {
		case err := <-exited:
			return userError("mount_failed", fmt.Sprintf("the mount daemon exited (%v): %s", err, daemonLogTail(logPath, 5)), "gs mount needs FUSE: on Linux /dev/fuse and fusermount3, on macOS macFUSE. Without it, use gs init.")
		case <-deadline:
			return userError("mount_failed", "the mount did not come up within 20 s: "+daemonLogTail(logPath, 5), "See "+logPath+".")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func mountIsUp(dir string) bool {
	if runtime.GOOS == "linux" {
		return isMounted(dir)
	}
	var st syscall.Statfs_t
	return syscall.Statfs(dir, &st) == nil && isMounted(dir)
}

func daemonLogTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// forceUnmount unmounts a mount whose daemon is gone.
func forceUnmount(dir string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		if err := exec.Command("umount", dir).Run(); err == nil {
			return nil
		}
		cmd = exec.Command("diskutil", "unmount", "force", dir)
	} else if path, err := exec.LookPath("fusermount3"); err == nil {
		cmd = exec.Command(path, "-u", dir)
	} else {
		cmd = exec.Command("fusermount", "-u", dir)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}
