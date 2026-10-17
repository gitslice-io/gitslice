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
	"syscall"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
	"gitslice.io/gitslice/internal/lazyfs/fusefs"
)

// serveMount serves a lazy workspace at its mount path until it is unmounted,
// asked to over the control socket, interrupted, or ctx ends.
func (r Runner) serveMount(ctx context.Context, rec mountRecord, debug bool, ready func()) error {
	state := r.mountStateDir(rec.ID)
	manifest, err := readManifest(filepath.Join(state, "manifest"), rec.BaseCommit)
	if err != nil {
		return err
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	layer, err := lazyfs.Open(filepath.Join(state, "local"), manifest, cacheContent{cache: cache}, lazyfs.Options{})
	if err != nil {
		return err
	}
	defer layer.Close()
	conn, err := dial(ctx, rec.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	fetcher := &mountFetcher{r: r, conn: conn, slice: rec.ref(), commitID: rec.BaseCommit, manifest: manifest, cache: cache, inflight: map[string]*fetchCall{}}
	mounted, err := fusefs.Mount(rec.MountPath, layer, fetcher, fusefs.Options{
		FsName: "gitslice:" + rec.Account + "/" + rec.Slice,
		UID:    uint32(os.Getuid()),
		GID:    uint32(os.Getgid()),
		Debug:  debug,
	})
	if err != nil {
		return userError("mount_failed", "cannot mount "+rec.MountPath+": "+err.Error(), "gs mount needs FUSE: on Linux /dev/fuse and fusermount3 (package fuse3), on macOS macFUSE. Without it, use gs init.")
	}
	mountedAt := time.Now().UTC()
	rec.MountedAt = mountedAt.Format(time.RFC3339)
	_ = r.writeMountRecord(rec)

	unmount := func() { _ = mounted.Server.Unmount() }
	socket := mountControlPath(rec.ID)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err == nil {
		_ = os.Remove(socket)
		if listener, err := net.Listen("unix", socket); err == nil {
			defer os.Remove(socket)
			defer listener.Close()
			go serveMountControl(listener, func() mountControlStatus {
				stats := mounted.Stats()
				fetcher.mu.Lock()
				files, bytes := fetcher.files, fetcher.bytes
				fetcher.mu.Unlock()
				return mountControlStatus{PID: os.Getpid(), MountedAt: mountedAt.Format(time.RFC3339), Opens: stats.Opens, Fetches: stats.Fetches, FetchedFiles: files, FetchedBytes: bytes, Errors: stats.Errors, LastError: stats.LastError}
			}, unmount)
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
	if ready != nil {
		ready()
	}
	mounted.Server.Wait()
	return nil
}

func serveMountControl(listener net.Listener, status func() mountControlStatus, unmount func()) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			var req mountControlRequest
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				return
			}
			_ = json.NewEncoder(conn).Encode(status())
			if req.Op == "unmount" {
				go unmount()
			}
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
		cmd = exec.Command("umount", dir)
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
