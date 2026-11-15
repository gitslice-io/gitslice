package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/cli"
)

// isolateClientCache lets each home keep its own content cache even when the
// environment names a shared one, so a test can tell what a mount fetched.
func isolateClientCache(t *testing.T) {
	t.Helper()
	t.Setenv("GS_CLIENT_CACHE_DIR", "")
	t.Setenv("GITSLICE_CLIENT_CACHE_DIR", "")
}

// setMountRuntimeDir creates an isolated 0700 runtime directory short enough
// for Unix domain sockets on both Linux and macOS and sets XDG_RUNTIME_DIR.
func setMountRuntimeDir(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if _, err := os.Stat(base); err != nil {
		base = ""
	}
	dir, err := os.MkdirTemp(base, "gsrun-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}

// startMountSupervisor runs the mount supervisor (gs mountd) in this process,
// with its sockets in a directory of its own, and waits for it to listen. The
// returned channel reports when it ends.
func startMountSupervisor(t *testing.T, home, cwd string) <-chan error {
	t.Helper()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = setMountRuntimeDir(t)
	} else {
		_ = os.Chmod(runtimeDir, 0o700)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var out strings.Builder
		r := cli.Runner{Home: home, Dir: cwd, Stdout: &out, Stderr: &out}
		done <- r.Run(ctx, []string{"mountd"})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	})
	for i := 0; i < 100; i++ {
		if matches, _ := filepath.Glob(filepath.Join(runtimeDir, "gs-mountd-*.sock")); len(matches) > 0 {
			return done
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the mount supervisor did not start listening")
	return done
}
