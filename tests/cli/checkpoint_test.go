//go:build linux

package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckpointRewindAndFork saves a lazy workspace's edits, rewinds to
// them (and back: a rewind saves what it replaces), and forks a checkpoint
// into a second workspace. Nothing in a checkpoint reaches the server.
func TestCheckpointRewindAndFork(t *testing.T) {
	isolateClientCache(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("GS_MOUNTD_IDLE", "1s")
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
	runCLI(t, home, upstream, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, upstream, "acme/payment/main.go", "package main\n")
	writeWorkspaceFile(t, upstream, "acme/payment/old.txt", "old\n")
	runCLI(t, home, upstream, "create", "--message", "files to mount", "--all", "--submit")

	startMountSupervisor(t, home, upstream)
	root := t.TempDir()
	dir := filepath.Join(root, "work")
	runCLI(t, home, upstream, "mount", "acme/payment", dir)
	app := filepath.Join(dir, "acme", "payment")
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(app, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(base, rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(base, rel))
		if err != nil {
			return "<missing>"
		}
		return string(data)
	}

	// Checkpoint 1: one edit and one delete.
	write("main.go", "package main // first\n")
	if err := os.Remove(filepath.Join(app, "old.txt")); err != nil {
		t.Fatal(err)
	}
	if out := runCLI(t, home, app, "checkpoint", "-m", "first try"); !strings.Contains(out, "checkpoint 1: 2 edits") {
		t.Fatalf("gs checkpoint: %q", out)
	}

	// More work, then rewind to the newest checkpoint that differs.
	write("main.go", "package main // second\n")
	write("extra.go", "package main // added later\n")
	if out := runCLI(t, home, app, "rewind"); !strings.Contains(out, "rewound to checkpoint 1") {
		t.Fatalf("gs rewind: %q", out)
	}
	if got := read(app, "main.go"); got != "package main // first\n" {
		t.Fatalf("main.go after rewind = %q", got)
	}
	if got := read(app, "extra.go"); got != "<missing>" {
		t.Fatalf("extra.go after rewind = %q, want it gone", got)
	}
	if got := read(app, "old.txt"); got != "<missing>" {
		t.Fatalf("old.txt after rewind = %q, want it still deleted", got)
	}

	// The rewind saved what it replaced: rewinding to that brings it back.
	var list struct {
		Checkpoints []struct {
			Number  int    `json:"number"`
			Message string `json:"message"`
			Auto    bool   `json:"auto"`
			Edits   int    `json:"edits"`
		} `json:"checkpoints"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, app, "checkpoint", "list", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Checkpoints) != 2 || list.Checkpoints[1].Message != "before gs rewind" || !list.Checkpoints[1].Auto || list.Checkpoints[1].Edits != 3 {
		t.Fatalf("checkpoints = %+v", list.Checkpoints)
	}
	runCLI(t, home, app, "rewind", "2")
	if read(app, "main.go") != "package main // second\n" || read(app, "extra.go") != "package main // added later\n" {
		t.Fatalf("rewind 2 did not bring the later work back: %q %q", read(app, "main.go"), read(app, "extra.go"))
	}

	// gs restore --all checkpoints first, so even it can be undone.
	runCLI(t, home, app, "restore", "--all")
	if got := read(app, "main.go"); got != "package main\n" {
		t.Fatalf("main.go after restore = %q", got)
	}
	runCLI(t, home, app, "rewind")
	if read(app, "extra.go") != "package main // added later\n" {
		t.Fatalf("rewind after restore did not bring the work back: %q", read(app, "extra.go"))
	}

	// A fork is a second workspace showing checkpoint 1.
	forkDir := filepath.Join(root, "fork")
	if out := runCLI(t, home, app, "fork", "1", forkDir); !strings.Contains(out, "forked checkpoint 1 into a new lazy workspace at "+forkDir) {
		t.Fatalf("gs fork: %q", out)
	}
	forkApp := filepath.Join(forkDir, "acme", "payment")
	if got := read(forkApp, "main.go"); got != "package main // first\n" {
		t.Fatalf("main.go in the fork = %q", got)
	}
	if got := read(forkApp, "extra.go"); got != "<missing>" {
		t.Fatalf("extra.go in the fork = %q", got)
	}
	if status := runCLI(t, home, forkApp, "status"); !strings.Contains(status, "main.go") || !strings.Contains(status, "old.txt") {
		t.Fatalf("gs status in the fork:\n%s", status)
	}
	// The original is untouched by the fork.
	if read(app, "extra.go") != "package main // added later\n" {
		t.Fatal("forking changed the original workspace")
	}
	runCLI(t, home, upstream, "unmount", forkDir)
	runCLI(t, home, upstream, "unmount", dir)

	// Checkpoints stay on this machine.
	blob := filepath.Join(ts.objectRoot, "blobs")
	if data, _ := exec.Command("grep", "-rl", "added later", blob).Output(); len(data) > 0 {
		t.Fatalf("checkpoint content reached the server: %s", data)
	}
}
