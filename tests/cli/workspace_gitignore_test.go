package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestWorkspaceHonorsGitignore checks that build output matched by .gitignore
// stays out of status and changesets, while tracked files that match a
// pattern keep reporting edits and deletes.
func TestWorkspaceHonorsGitignore(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	importer := t.TempDir()
	loginTestCLI(t, ts, home, importer)

	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	writeFile := func(dir, rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("git", "init", "-q")
	writeFile(repo, ".gitignore", "node_modules/\n*.log\n")
	writeFile(repo, "main.go", "package main\n")
	writeFile(repo, "keep.log", "tracked before the ignore rule\n")
	run("git", "add", "-A")
	run("git", "add", "-f", "keep.log")
	run("git", "commit", "-q", "-m", "ignored and tracked")
	runCLI(t, home, importer, "import", repo, "--mount", "/acme/payment/app", "--slice", "acme/payment", "--mode", "deep")

	workspace := t.TempDir()
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	dir := filepath.Join(workspace, "acme", "payment", "app")
	if _, err := os.Stat(filepath.Join(dir, "keep.log")); err != nil {
		t.Fatalf("tracked keep.log should be materialized: %v", err)
	}
	writeFile(dir, "node_modules/pkg/index.js", "module.exports = 1\n")
	writeFile(dir, "debug.log", "noise\n")
	writeFile(dir, "new.go", "package main\n")
	writeFile(dir, "keep.log", "edited\n")

	status := runCLI(t, home, workspace, "status")
	for _, ignored := range []string{"node_modules", "debug.log"} {
		if strings.Contains(status, ignored) {
			t.Fatalf("status should not list ignored %s:\n%s", ignored, status)
		}
	}
	for _, changed := range []string{"/acme/payment/app/new.go", "/acme/payment/app/keep.log"} {
		if !strings.Contains(status, changed) {
			t.Fatalf("status should list %s:\n%s", changed, status)
		}
	}

	runCLI(t, home, workspace, "create", "--message", "new.go and keep.log", "--all")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	draft := singleDraftChangeset(t, grpcAuthContext(readToken(t, home)), corev1.NewChangesetServiceClient(conn))
	changed := append([]string(nil), draft.Patchsets[len(draft.Patchsets)-1].ChangedPaths...)
	sort.Strings(changed)
	want := []string{"/acme/payment/app/keep.log", "/acme/payment/app/new.go"}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("changeset paths = %v, want %v", changed, want)
	}

	if err := os.Remove(filepath.Join(dir, "keep.log")); err != nil {
		t.Fatal(err)
	}
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/app/keep.log") {
		t.Fatalf("deleting tracked keep.log should show in status:\n%s", status)
	}
}
