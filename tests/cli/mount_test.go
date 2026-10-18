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

// TestMountSyncsAndSwitchesThroughItsLayers moves a mount's base with gs sync
// and switches between the changesets of a dependency tree, checking each
// time that the mount shows what a gs init workspace would.
func TestMountSyncsAndSwitchesThroughItsLayers(t *testing.T) {
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
	upstream := t.TempDir()
	loginTestCLI(t, ts, home, upstream)
	lines := func(ls ...string) string { return strings.Join(ls, "\n") + "\n" }
	runCLI(t, home, upstream, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, upstream, "acme/payment/src/main.go", lines("one", "two", "three", "four", "five", "six", "seven"))
	writeWorkspaceFile(t, upstream, "acme/payment/src/util.go", "package util // v1\n")
	writeWorkspaceFile(t, upstream, "acme/payment/old.txt", "old\n")
	writeWorkspaceFile(t, upstream, "acme/payment/.gitignore", "out/\n")
	writeWorkspaceFile(t, upstream, "acme/payment/notes.txt", "notes\n")
	runCLI(t, home, upstream, "create", "--message", "files to mount", "--all", "--submit")

	// The mount has a home, and so a cache, of its own: what it shows, it
	// fetched.
	mountHome := t.TempDir()
	loginTestCLI(t, ts, mountHome, upstream)
	dir := filepath.Join(t.TempDir(), "payment")
	startMount(t, mountHome, upstream, "--prefetch", "acme/payment/src/**", "acme/payment", dir)
	app := filepath.Join(dir, "acme", "payment")
	// --prefetch fetches src/ in the background, before anything reads it.
	for i := 0; ; i++ {
		var status struct {
			Daemon struct {
				FetchedFiles int `json:"fetched_files"`
			} `json:"daemon"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, mountHome, upstream, "mount", "status", dir, "--json")), &status); err != nil {
			t.Fatal(err)
		}
		if status.Daemon.FetchedFiles == 2 {
			break
		}
		if i == 100 {
			t.Fatalf("--prefetch fetched %d files, want 2 (src/main.go and src/util.go)", status.Daemon.FetchedFiles)
		}
		time.Sleep(50 * time.Millisecond)
	}
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(app, rel))
		if err != nil {
			t.Fatalf("read %s through the mount: %v", rel, err)
		}
		return string(data)
	}
	gone := func(rel string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(app, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s should not be in the mount (err = %v)", rel, err)
		}
	}
	localEdits := func() int {
		t.Helper()
		var status struct {
			LocalEdits int `json:"local_edits"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, mountHome, app, "mount", "status", "--json")), &status); err != nil {
			t.Fatal(err)
		}
		return status.LocalEdits
	}
	upstreamChange := func(message string, files map[string]string, remove ...string) {
		t.Helper()
		runCLI(t, home, upstream, "sync")
		for rel, content := range files {
			writeWorkspaceFile(t, upstream, "acme/payment/"+rel, content)
		}
		for _, rel := range remove {
			if err := os.Remove(filepath.Join(upstream, "acme", "payment", rel)); err != nil {
				t.Fatal(err)
			}
		}
		runCLI(t, home, upstream, "create", "--message", message, "--all", "--submit")
	}

	// Appending to a file nothing has read copies its content up, fetching it.
	notes, err := os.OpenFile(filepath.Join(app, "notes.txt"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notes.WriteString("more\n"); err != nil {
		t.Fatal(err)
	}
	if err := notes.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read("notes.txt"); got != "notes\nmore\n" {
		t.Fatalf("notes.txt after appending = %q", got)
	}
	runCLI(t, mountHome, app, "restore", "notes.txt")

	// Upstream moves after the mount read a file, whose pages the kernel
	// keeps: gs sync must make it forget them.
	if got := read("src/util.go"); got != "package util // v1\n" {
		t.Fatalf("util.go = %q", got)
	}
	upstreamChange("upstream v2", map[string]string{"src/util.go": "package util // v2, longer\n", "docs/new.md": "# New\n"}, "old.txt")
	runCLI(t, mountHome, app, "sync")
	if got := read("src/util.go"); got != "package util // v2, longer\n" {
		t.Fatalf("util.go after gs sync = %q", got)
	}
	if got := read("docs/new.md"); got != "# New\n" {
		t.Fatalf("docs/new.md after gs sync = %q", got)
	}
	gone("old.txt")
	if n := localEdits(); n != 0 {
		t.Fatalf("%d local edits after syncing an unchanged mount", n)
	}

	// A captured change and an upstream change to another line of the same
	// file: gs sync merges them.
	if err := os.WriteFile(filepath.Join(app, "src", "main.go"), []byte(lines("ONE", "two", "three", "four", "five", "six", "seven")), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, mountHome, app, "create", "--message", "local edits line one", "--all")
	upstreamChange("upstream edits line seven", map[string]string{"src/main.go": lines("one", "two", "three", "four", "five", "six", "SEVEN")})
	runCLI(t, mountHome, app, "sync")
	merged := lines("ONE", "two", "three", "four", "five", "six", "SEVEN")
	if got := read("src/main.go"); got != merged {
		t.Fatalf("main.go after gs sync = %q, want %q", got, merged)
	}

	// A second changeset on the first: gs down shows the first alone, gs up
	// the second again; neither writes or fetches the files themselves.
	if err := os.WriteFile(filepath.Join(app, "src", "second.go"), []byte("package main // second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, mountHome, app, "create", "--message", "second change", "--all")
	if err := os.MkdirAll(filepath.Join(app, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "out", "build.log"), []byte("ignored build output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, mountHome, app, "down")
	gone("src/second.go")
	if got := read("src/main.go"); got != merged {
		t.Fatalf("main.go on the first changeset = %q", got)
	}
	if got := read("out/build.log"); got != "ignored build output\n" {
		t.Fatalf("ignored build output after gs down = %q", got)
	}
	runCLI(t, mountHome, app, "up")
	if got := read("src/second.go"); got != "package main // second\n" {
		t.Fatalf("second.go after gs up = %q", got)
	}
	if status := runCLI(t, mountHome, app, "status"); strings.Contains(status, "build.log") {
		t.Fatalf("gs status after gs up shows the ignored build output:\n%s", status)
	}

	// Land both and sync: what landed leaves the local layer.
	runCLI(t, mountHome, app, "submit")
	runCLI(t, mountHome, app, "sync")
	if got := read("src/second.go"); got != "package main // second\n" {
		t.Fatalf("second.go after landing = %q", got)
	}
	if n := localEdits(); n != 1 {
		t.Fatalf("%d local edits after landing; want 1, the ignored build output", n)
	}
	verify := t.TempDir()
	runCLI(t, home, verify, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verify, "acme/payment/src/main.go", merged)
	assertWorkspaceFile(t, verify, "acme/payment/src/second.go", "package main // second\n")

	// The moved layers survive a remount.
	runCLI(t, mountHome, upstream, "unmount", dir)
	startMount(t, mountHome, upstream, "acme/payment", dir)
	if got := read("src/main.go"); got != merged {
		t.Fatalf("main.go after remounting = %q", got)
	}
	if got := read("out/build.log"); got != "ignored build output\n" {
		t.Fatalf("build output after remounting = %q", got)
	}
}

// TestMountSyncMergesAChangesetOutsideATree syncs a mount whose changeset is
// not in a dependency tree (gs cs create): the mount merges in its own local
// layer, conflicts get markers there, and landing empties it.
func TestMountSyncMergesAChangesetOutsideATree(t *testing.T) {
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
	upstream := t.TempDir()
	loginTestCLI(t, ts, home, upstream)
	lines := func(ls ...string) string { return strings.Join(ls, "\n") + "\n" }
	runCLI(t, home, upstream, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, upstream, "acme/payment/merge.txt", lines("one", "two", "three", "four", "five", "six", "seven"))
	writeWorkspaceFile(t, upstream, "acme/payment/conflict.txt", lines("alpha", "beta"))
	runCLI(t, home, upstream, "create", "--message", "files to mount", "--all", "--submit")

	mountHome := t.TempDir()
	loginTestCLI(t, ts, mountHome, upstream)
	dir := filepath.Join(t.TempDir(), "payment")
	startMount(t, mountHome, upstream, "acme/payment", dir)
	app := filepath.Join(dir, "acme", "payment")
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(app, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(app, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	write("merge.txt", lines("ONE", "two", "three", "four", "five", "six", "seven"))
	write("conflict.txt", lines("alpha", "BETA from the mount"))
	runCLI(t, mountHome, app, "cs", "create", "--title", "edits in a mount")

	runCLI(t, home, upstream, "sync")
	writeWorkspaceFile(t, upstream, "acme/payment/merge.txt", lines("one", "two", "three", "four", "five", "six", "SEVEN"))
	writeWorkspaceFile(t, upstream, "acme/payment/conflict.txt", lines("alpha", "BETA upstream"))
	writeWorkspaceFile(t, upstream, "acme/payment/added.txt", "added upstream\n")
	runCLI(t, home, upstream, "create", "--message", "upstream edits", "--all", "--submit")

	out := runCLI(t, mountHome, app, "sync")
	if !strings.Contains(out, "conflict.txt") {
		t.Fatalf("gs sync should report conflict.txt:\n%s", out)
	}
	if got, want := read("merge.txt"), lines("ONE", "two", "three", "four", "five", "six", "SEVEN"); got != want {
		t.Fatalf("merge.txt after gs sync = %q, want %q", got, want)
	}
	if got := read("conflict.txt"); !strings.Contains(got, "<<<<<<< gitslice local") || !strings.Contains(got, "BETA from the mount") || !strings.Contains(got, "BETA upstream") {
		t.Fatalf("conflict.txt after gs sync has no conflict markers:\n%s", got)
	}
	if got := read("added.txt"); got != "added upstream\n" {
		t.Fatalf("added.txt after gs sync = %q", got)
	}

	// Resolve, capture, land: what landed leaves the local layer.
	write("conflict.txt", lines("alpha", "BETA resolved"))
	runCLI(t, mountHome, app, "cs", "update")
	runCLI(t, mountHome, app, "cs", "submit")
	var status struct {
		LocalEdits int `json:"local_edits"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, mountHome, app, "mount", "status", "--json")), &status); err != nil {
		t.Fatal(err)
	}
	if status.LocalEdits != 0 {
		t.Fatalf("%d local edits after landing everything", status.LocalEdits)
	}
	if got := read("conflict.txt"); got != lines("alpha", "BETA resolved") {
		t.Fatalf("conflict.txt after landing = %q", got)
	}
}
