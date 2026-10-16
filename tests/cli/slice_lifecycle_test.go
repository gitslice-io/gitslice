package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedLifecycleSlice lands one file under /acme/payment/<name> and creates a
// slice for that path, as someone starting a small project would.
func seedLifecycleSlice(t *testing.T, home, workspace, name string) {
	t.Helper()
	writeWorkspaceFile(t, workspace, "acme/payment/"+name+"/readme.md", "# "+name+"\n")
	runCLI(t, home, workspace, "create", "--all", "--message", "add "+name, "--submit")
	runCLI(t, home, workspace, "slice", "create", "acme/"+name, "--include", "/acme/payment/"+name)
}

// TestSliceRenameKeepsTheOldNameWorking renames a slice and checks what has to
// keep working: the old name in the CLI, in a workspace made before the rename,
// and as a Git URL.
func TestSliceRenameKeepsTheOldNameWorking(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	seedLifecycleSlice(t, home, workspace, "bot-channels")

	// A workspace made under the old name, before the rename.
	before := t.TempDir()
	runCLI(t, home, before, "workspace", "init", "acme/bot-channels")

	out := runCLI(t, home, workspace, "slice", "rename", "acme/bot-channels", "botparley")
	for _, want := range []string{"renamed slice acme:bot-channels to acme:botparley", "old name acme:bot-channels still resolves"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rename output missing %q:\n%s", want, out)
		}
	}
	info := runCLI(t, home, workspace, "slice", "info", "acme/bot-channels")
	for _, want := range []string{"ref: acme:botparley", "former_names: bot-channels"} {
		if !strings.Contains(info, want) {
			t.Fatalf("info through the old name missing %q:\n%s", want, info)
		}
	}
	list := runCLI(t, home, workspace, "slice", "list", "acme")
	if !strings.Contains(list, "acme:botparley") || strings.Contains(list, "acme:bot-channels\n") {
		t.Fatalf("list after rename:\n%s", list)
	}
	_, stderr := runCLIFails(t, home, workspace, "slice", "create", "acme/bot-channels", "--include", "/acme/payment/bot-channels")
	if !strings.Contains(stderr, "reserved") {
		t.Fatalf("creating a slice with the old name: %s", stderr)
	}
	_, stderr = runCLIFails(t, home, workspace, "slice", "rename", "acme/botparley", "payment")
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("renaming onto a slice in use: %s", stderr)
	}

	// The workspace made before the rename keeps working under the old name.
	writeWorkspaceFile(t, before, "acme/payment/bot-channels/new.txt", "after the rename\n")
	runCLI(t, home, before, "create", "--all", "--message", "work after the rename", "--submit")
	var changed struct {
		Slice string `json:"slice"`
	}
	if err := readJSONFile(filepath.Join(before, ".gs", "slice.json"), &changed); err != nil || changed.Slice != "bot-channels" {
		t.Fatalf("an untouched workspace keeps its recorded name: %+v, %v", changed, err)
	}

	// Git clones work through the old and the new name.
	header := "http.extraHeader=Authorization: Bearer " + readToken(t, home)
	heads := map[string]string{}
	for _, name := range []string{"bot-channels", "botparley"} {
		clone := filepath.Join(t.TempDir(), name)
		runGit(t, "", "-c", header, "clone", "http://"+ts.gitAddr+"/git/acme/"+name+".git", clone)
		if _, err := os.Stat(filepath.Join(clone, "acme", "payment", "bot-channels", "readme.md")); err != nil {
			t.Fatalf("clone through %q is missing the slice's files: %v", name, err)
		}
		heads[name] = strings.TrimSpace(runGit(t, "", "-C", clone, "rev-parse", "HEAD"))
	}
	if heads["bot-channels"] != heads["botparley"] {
		t.Fatalf("the old and new names serve different Git history: %v", heads)
	}

	// Renaming from inside a workspace of the slice points it at the new name.
	inside := t.TempDir()
	runCLI(t, home, inside, "workspace", "init", "acme/botparley")
	runCLI(t, home, inside, "slice", "rename", "acme/botparley", "parley")
	if err := readJSONFile(filepath.Join(inside, ".gs", "slice.json"), &changed); err != nil || changed.Slice != "parley" {
		t.Fatalf("the workspace should follow the rename: %+v, %v", changed, err)
	}
}

// TestSliceArchiveHidesAndFreezes archives a slice, checks it leaves listings
// and refuses changes, and restores it.
func TestSliceArchiveHidesAndFreezes(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	seedLifecycleSlice(t, home, workspace, "old-ideas")
	slice := t.TempDir()
	runCLI(t, home, slice, "workspace", "init", "acme/old-ideas")

	if out := runCLI(t, home, workspace, "slice", "archive", "acme/old-ideas"); !strings.Contains(out, "archived slice acme:old-ideas") {
		t.Fatalf("archive output:\n%s", out)
	}
	if list := runCLI(t, home, workspace, "slice", "list", "acme"); strings.Contains(list, "old-ideas") {
		t.Fatalf("an archived slice is listed:\n%s", list)
	}
	if list := runCLI(t, home, workspace, "slice", "list", "acme", "--archived"); !strings.Contains(list, "acme:old-ideas (archived)") {
		t.Fatalf("--archived should list it:\n%s", list)
	}
	var archived struct {
		Archived bool `json:"archived"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "slice", "info", "acme/old-ideas", "--json")), &archived); err != nil || !archived.Archived {
		t.Fatalf("slice info --json = %+v, %v", archived, err)
	}
	writeWorkspaceFile(t, slice, "acme/payment/old-ideas/idea.txt", "a new idea\n")
	_, stderr := runCLIFails(t, home, slice, "create", "--all", "--message", "idea")
	if !strings.Contains(stderr, "archived and read-only") {
		t.Fatalf("a change in an archived slice: %s", stderr)
	}

	if out := runCLI(t, home, workspace, "slice", "unarchive", "acme/old-ideas"); !strings.Contains(out, "unarchived slice acme:old-ideas") {
		t.Fatalf("unarchive output:\n%s", out)
	}
	if list := runCLI(t, home, workspace, "slice", "list", "acme"); !strings.Contains(list, "acme:old-ideas") {
		t.Fatalf("an unarchived slice should be listed:\n%s", list)
	}
	runCLI(t, home, slice, "create", "--all", "--message", "idea", "--submit")
}

// TestSliceDeleteWithChangesetsKeepsHistory deletes a slice that has landed
// changesets, which used to be refused.
func TestSliceDeleteWithChangesetsKeepsHistory(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	seedLifecycleSlice(t, home, workspace, "scratch")
	slice := t.TempDir()
	runCLI(t, home, slice, "workspace", "init", "acme/scratch")
	writeWorkspaceFile(t, slice, "acme/payment/scratch/notes.txt", "notes\n")
	runCLI(t, home, slice, "create", "--all", "--message", "notes from the slice", "--submit")
	writeWorkspaceFile(t, slice, "acme/payment/scratch/open.txt", "still open\n")
	runCLI(t, home, slice, "create", "--all", "--message", "an open changeset")

	_, stderr := runCLIFails(t, home, workspace, "slice", "delete", "acme/scratch", "--yes")
	if !strings.Contains(stderr, "open changeset") {
		t.Fatalf("deleting a slice with an open changeset: %s", stderr)
	}
	runCLI(t, home, slice, "cs", "abandon")

	out := runCLI(t, home, workspace, "slice", "delete", "acme/scratch", "--yes")
	for _, want := range []string{"deleted slice acme:scratch", "its changesets remain"} {
		if !strings.Contains(out, want) {
			t.Fatalf("delete output missing %q:\n%s", want, out)
		}
	}
	if _, stderr := runCLIFails(t, home, workspace, "slice", "info", "acme/scratch"); !strings.Contains(stderr, "not found") {
		t.Fatalf("a deleted slice should be gone: %s", stderr)
	}
	// The files and their history stay on the path.
	verify := t.TempDir()
	runCLI(t, home, verify, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verify, "acme/payment/scratch/notes.txt", "notes\n")
}
