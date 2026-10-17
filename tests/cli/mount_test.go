//go:build linux

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/cli"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
)

func mounted(dir string) bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 4 && fields[4] == dir {
			return true
		}
	}
	return false
}

// startMount serves gs mount --foreground in the background and waits for the
// mount to come up. The returned channel reports when it ends.
func startMount(t *testing.T, home, cwd string, args ...string) <-chan error {
	t.Helper()
	dir := args[len(args)-1]
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var out strings.Builder
		r := cli.Runner{Home: home, Dir: cwd, Stdout: &out, Stderr: &out}
		done <- r.Run(ctx, append([]string{"mount", "--foreground"}, args...))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	for i := 0; i < 200 && !mounted(dir); i++ {
		select {
		case err := <-done:
			if err != nil && strings.Contains(err.Error(), "cannot mount") {
				t.Skipf("FUSE is not usable here: %v", err)
			}
			t.Fatalf("gs mount ended early: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !mounted(dir) {
		t.Fatal("the mount did not come up")
	}
	return done
}

// TestMountServesASliceLazily mounts a slice, reads it through the mount, and
// checks what was fetched, then unmounts and re-attaches the same workspace.
func TestMountServesASliceLazily(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse here")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("no fusermount here")
		}
	}
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/payment/README.md", "# Payment\n")
	writeWorkspaceFile(t, workspace, "acme/payment/src/main.go", "package main\n")
	writeWorkspaceFile(t, workspace, "acme/payment/src/util.go", "package main // util\n")
	writeWorkspaceFile(t, workspace, "acme/payment/docs/guide.md", "# Guide\n")
	runCLI(t, home, workspace, "create", "--all", "--message", "files to mount", "--submit")

	dir := filepath.Join(t.TempDir(), "payment")
	done := startMount(t, home, workspace, "--read-only", "acme/payment", dir)

	var status struct {
		ID      string `json:"id"`
		Mounted bool   `json:"mounted"`
		Files   int    `json:"files"`
		Daemon  *struct {
			Opens   int64 `json:"opens"`
			Fetches int64 `json:"fetches"`
		} `json:"daemon"`
	}
	readStatus := func() {
		t.Helper()
		if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "mount", "status", dir, "--json")), &status); err != nil {
			t.Fatal(err)
		}
	}
	readStatus()
	if !status.Mounted || status.Files < 4 || status.Daemon == nil || status.Daemon.Fetches != 0 {
		t.Fatalf("status before any read = %+v", status)
	}
	id := status.ID

	// Listing costs nothing; the first read of a file fetches it and the small
	// files beside it.
	entries, err := os.ReadDir(filepath.Join(dir, "acme", "payment", "src"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("listing src = %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "acme", "payment", "src", "main.go"))
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("read main.go = %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "acme", "payment", "src", "util.go"))
	if err != nil || string(data) != "package main // util\n" {
		t.Fatalf("read util.go = %q, %v", data, err)
	}
	readStatus()
	if status.Daemon.Fetches != 0 && status.Daemon.Fetches != 1 {
		t.Fatalf("reading two files of one directory fetched %d times, want at most one", status.Daemon.Fetches)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme", "payment", "README.md"), []byte("x"), 0o644); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("write: err = %v, want EROFS", err)
	}

	// gs fetch warms the rest from inside the mount.
	out := runCLI(t, home, filepath.Join(dir, "acme", "payment"), "fetch")
	if !strings.Contains(out, "fetched ") {
		t.Fatalf("gs fetch output: %s", out)
	}
	if list := runCLI(t, home, workspace, "mount", "list"); !strings.Contains(list, id) || !strings.Contains(list, "acme:payment") || !strings.Contains(list, "mounted,") {
		t.Fatalf("gs mount list:\n%s", list)
	}

	if out := runCLI(t, home, workspace, "unmount", dir); !strings.Contains(out, "unmounted "+dir) {
		t.Fatalf("gs unmount: %s", out)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gs mount returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("gs mount did not return after the unmount")
	}
	if list := runCLI(t, home, workspace, "mount", "list"); !strings.Contains(list, "not mounted") {
		t.Fatalf("after unmount, gs mount list:\n%s", list)
	}

	// Mounting the same directory again re-attaches the same workspace, with
	// everything already in the cache.
	startMount(t, home, workspace, "--read-only", "acme/payment", dir)
	readStatus()
	if status.ID != id {
		t.Fatalf("remount made a new workspace %s, want %s", status.ID, id)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "acme", "payment", "docs", "guide.md")); err != nil || string(data) != "# Guide\n" {
		t.Fatalf("read guide.md after remount = %q, %v", data, err)
	}
	readStatus()
	if status.Daemon.Fetches != 0 {
		t.Fatalf("after gs fetch, reads should not fetch: %+v", status.Daemon)
	}
	runCLI(t, home, workspace, "unmount", dir)
}

// TestMountKeepsEditsLocalUntilCreate edits a slice through a mount with
// ordinary file operations and checks that nothing reaches the server until
// gs create, which then lands exactly those edits.
func TestMountKeepsEditsLocalUntilCreate(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse here")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("no fusermount here")
		}
	}
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/payment/README.md", "# Payment\n")
	writeWorkspaceFile(t, workspace, "acme/payment/src/main.go", "package main\n")
	writeWorkspaceFile(t, workspace, "acme/payment/docs/guide.md", "# Guide\n")
	writeWorkspaceFile(t, workspace, "acme/payment/old.txt", "old\n")
	runCLI(t, home, workspace, "create", "--all", "--message", "files to mount", "--submit")

	dir := filepath.Join(t.TempDir(), "payment")
	startMount(t, home, workspace, "acme/payment", dir)
	app := filepath.Join(dir, "acme", "payment")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Edits with ordinary file operations.
	newContent := "package main\n\nfunc New() {} // only on this machine\n"
	must(os.WriteFile(filepath.Join(app, "src", "main.go"), []byte("package main // edited\n"), 0o644))
	must(os.WriteFile(filepath.Join(app, "src", "new.go"), []byte(newContent), 0o644))
	must(os.Remove(filepath.Join(app, "old.txt")))
	must(os.Rename(filepath.Join(app, "docs"), filepath.Join(app, "manual")))

	// Nothing has left the machine: the server has no copy of the new file.
	blob := filepath.Join(ts.objectRoot, filepath.FromSlash(filesystem.BlobKey(objectid.RawContentHash([]byte(newContent)))))
	if _, err := os.Stat(blob); err == nil {
		t.Fatal("the new file reached the server before gs create")
	}
	status := runCLI(t, home, app, "status")
	for _, want := range []string{"src/main.go", "src/new.go", "old.txt", "docs/guide.md", "manual/guide.md"} {
		if !strings.Contains(status, want) {
			t.Fatalf("gs status inside the mount is missing %s:\n%s", want, status)
		}
	}
	if diff := runCLI(t, home, app, "diff"); !strings.Contains(diff, "+package main // edited") || !strings.Contains(diff, "only on this machine") {
		t.Fatalf("gs diff inside the mount:\n%s", diff)
	}
	if _, err := os.Stat(blob); err == nil {
		t.Fatal("gs status or gs diff uploaded content")
	}

	// gs restore drops a local edit.
	must(os.WriteFile(filepath.Join(app, "README.md"), []byte("scratch\n"), 0o644))
	runCLI(t, home, app, "restore", "README.md")
	if data, _ := os.ReadFile(filepath.Join(app, "README.md")); string(data) != "# Payment\n" {
		t.Fatalf("README.md after gs restore = %q", data)
	}

	// Commands that rewrite the workspace refuse, for now.
	if _, stderr := runCLIFails(t, home, app, "sync"); !strings.Contains(stderr, "does not work in a lazy workspace") {
		t.Fatalf("gs sync in a mount: %s", stderr)
	}

	// gs create uploads and lands exactly the edits.
	runCLI(t, home, app, "create", "--all", "--message", "edits from a lazy workspace", "--submit")
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("after gs create the server should have the new file: %v", err)
	}
	verify := t.TempDir()
	runCLI(t, home, verify, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verify, "acme/payment/src/main.go", "package main // edited\n")
	assertWorkspaceFile(t, verify, "acme/payment/src/new.go", newContent)
	assertWorkspaceFile(t, verify, "acme/payment/manual/guide.md", "# Guide\n")
	assertWorkspaceFile(t, verify, "acme/payment/README.md", "# Payment\n")
	for _, gone := range []string{"acme/payment/old.txt", "acme/payment/docs"} {
		if _, err := os.Stat(filepath.Join(verify, gone)); err == nil {
			t.Fatalf("%s should be gone after the landing", gone)
		}
	}

	// Edits survive an unmount: the local layer is on disk.
	must(os.WriteFile(filepath.Join(app, "src", "later.go"), []byte("package main // later\n"), 0o644))
	runCLI(t, home, workspace, "unmount", dir)
	startMount(t, home, workspace, "acme/payment", dir)
	if data, err := os.ReadFile(filepath.Join(app, "src", "later.go")); err != nil || string(data) != "package main // later\n" {
		t.Fatalf("an edit after remounting = %q, %v", data, err)
	}

	// gs mount discard deletes an unmounted workspace, after confirmation.
	runCLI(t, home, workspace, "unmount", dir)
	if _, stderr := runCLIFails(t, home, workspace, "mount", "discard", dir); !strings.Contains(stderr, "--yes") {
		t.Fatalf("discard without --yes: %s", stderr)
	}
	runCLI(t, home, workspace, "mount", "discard", dir, "--yes")
	if list := runCLI(t, home, workspace, "mount", "list"); !strings.Contains(list, "no lazy workspaces") {
		t.Fatalf("after discard:\n%s", list)
	}
}
