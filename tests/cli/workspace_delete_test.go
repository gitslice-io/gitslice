package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gs workspace delete removes a checkout, refusing while it has edits no
// changeset holds; once they are in a changeset (even landed) it goes.
func TestWorkspaceDeleteCheckout(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	elsewhere := t.TempDir()
	loginTestCLI(t, ts, home, elsewhere)
	newWorkspace := func(name string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runCLI(t, home, dir, "workspace", "init", "acme/payment")
		return dir
	}

	// A clean workspace goes at once, with everything in it.
	clean := newWorkspace("clean")
	writeWorkspaceFile(t, clean, "build/out.bin", "ignored or not, it goes too")
	if err := os.Remove(filepath.Join(clean, "build", "out.bin")); err != nil {
		t.Fatal(err)
	}
	if out := runCLI(t, home, elsewhere, "workspace", "delete", clean); !strings.Contains(out, "deleted checkout workspace "+clean) {
		t.Fatalf("delete a clean workspace: %s", out)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Fatalf("the workspace directory is still there: %v", err)
	}

	// Local edits no changeset holds: refused, named, nothing deleted.
	dirty := newWorkspace("dirty")
	writeWorkspaceFile(t, dirty, "notes/todo.md", "keep me\n")
	if _, stderr := runCLIFails(t, home, elsewhere, "workspace", "delete", dirty); !strings.Contains(stderr, "1 local edit that no changeset holds") || !strings.Contains(stderr, "notes/todo.md") {
		t.Fatalf("delete with an unsaved edit: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(dirty, "notes", "todo.md")); err != nil {
		t.Fatalf("a refused delete removed files: %v", err)
	}
	// A folder inside the workspace is not the workspace.
	if _, stderr := runCLIFails(t, home, elsewhere, "workspace", "delete", filepath.Join(dirty, "notes")); !strings.Contains(stderr, "is inside the workspace at "+dirty) {
		t.Fatalf("delete a folder inside a workspace: %s", stderr)
	}

	// Once the edit is in a changeset, and landed, the workspace goes
	// without --force: nothing is lost.
	runCLI(t, home, dirty, "create", "--all", "--message", "notes", "--submit")
	var deleted struct {
		Path      string `json:"path"`
		Kind      string `json:"kind"`
		Deleted   bool   `json:"deleted"`
		Discarded int    `json:"discarded_edits"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, elsewhere, "ws", "rm", dirty, "--json")), &deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted || deleted.Kind != "checkout" || deleted.Path != dirty || deleted.Discarded != 0 {
		t.Fatalf("delete after the edit landed: %+v", deleted)
	}

	// --force discards what no changeset holds.
	forced := newWorkspace("forced")
	writeWorkspaceFile(t, forced, "scratch.txt", "throwaway\n")
	if out := runCLI(t, home, elsewhere, "workspace", "delete", forced, "--force"); !strings.Contains(out, "discarding 1 local edit") {
		t.Fatalf("delete --force: %s", out)
	}
	if _, err := os.Stat(forced); !os.IsNotExist(err) {
		t.Fatalf("--force left the workspace: %v", err)
	}
	// From inside it, with no argument.
	here := newWorkspace("here")
	if out := runCLI(t, home, here, "workspace", "delete"); !strings.Contains(out, "your shell was in it") {
		t.Fatalf("delete from inside: %s", out)
	}
}

// A lazy workspace is unmounted, its state deleted and its empty mount
// directory removed; edits in its local layer stop it, mounted or not.
func TestWorkspaceDeleteLazy(t *testing.T) {
	isolateClientCache(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
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
	elsewhere := t.TempDir()
	loginTestCLI(t, ts, home, elsewhere)
	startMountSupervisor(t, home, elsewhere)
	mounts := func() []string {
		t.Helper()
		var list struct {
			Workspaces []struct {
				MountPath string `json:"mount_path"`
			} `json:"workspaces"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, home, elsewhere, "mount", "list", "--json")), &list); err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, m := range list.Workspaces {
			paths = append(paths, m.MountPath)
		}
		return paths
	}
	contains := func(list []string, s string) bool {
		for _, v := range list {
			if v == s {
				return true
			}
		}
		return false
	}

	dir := filepath.Join(t.TempDir(), "lazy")
	runCLI(t, home, elsewhere, "init", "--lazy", "acme/payment", dir)
	writeWorkspaceFile(t, dir, "notes/draft.md", "not saved\n")
	// Mounted, with an edit: refused, still mounted.
	if _, stderr := runCLIFails(t, home, elsewhere, "workspace", "delete", dir); !strings.Contains(stderr, "local edit that no changeset holds") || !strings.Contains(stderr, "draft.md") {
		t.Fatalf("delete a mounted lazy workspace with an edit: %s", stderr)
	}
	// Unmounted, the edit is still in its local layer: still refused.
	runCLI(t, home, elsewhere, "unmount", dir)
	if _, stderr := runCLIFails(t, home, elsewhere, "workspace", "delete", dir); !strings.Contains(stderr, "draft.md") {
		t.Fatalf("delete an unmounted lazy workspace with an edit: %s", stderr)
	}
	runCLI(t, home, elsewhere, "mount", dir)
	if out := runCLI(t, home, elsewhere, "workspace", "delete", dir, "--force"); !strings.Contains(out, "deleted lazy workspace "+dir) {
		t.Fatalf("delete --force a mounted lazy workspace: %s", out)
	}
	if contains(mounts(), dir) {
		t.Fatalf("gs mount list still has %s", dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the empty mount directory was left: %v", err)
	}

	// A clean one goes at once.
	clean := filepath.Join(t.TempDir(), "clean")
	runCLI(t, home, elsewhere, "init", "--lazy", "acme/payment", clean)
	if out := runCLI(t, home, elsewhere, "workspace", "delete", clean); !strings.Contains(out, "deleted lazy workspace") {
		t.Fatalf("delete a clean lazy workspace: %s", out)
	}
	if contains(mounts(), clean) {
		t.Fatalf("gs mount list still has %s", clean)
	}
}
