package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestWorkspacePreservesSymlinks checks that workspaces materialize symlinks
// and never turn them into regular files on the next changeset.
func TestWorkspacePreservesSymlinks(t *testing.T) {
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
	run("git", "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "target.md"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.md", filepath.Join(repo, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(repo, "abs-link")); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-q", "-m", "symlinks")
	runCLI(t, home, importer, "import", repo, "--mount", "/acme/payment/sym", "--slice", "acme/payment", "--mode", "deep")

	workspace := t.TempDir()
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	dir := filepath.Join(workspace, "acme", "payment", "sym")
	if link, err := os.Readlink(filepath.Join(dir, "link.md")); err != nil || link != "target.md" {
		t.Fatalf("link.md should be a symlink to target.md, got %q, %v", link, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "abs-link")); err != nil || string(data) != "/etc/hostname" {
		t.Fatalf("an absolute symlink should materialize as a file holding its target, got %q, %v", data, err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "abs-link")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("abs-link should not be a real symlink: %v %v", info, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, workspace, "create", "--message", "only new.txt", "--all")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	draft := singleDraftChangeset(t, grpcAuthContext(readToken(t, home)), corev1.NewChangesetServiceClient(conn))
	changed := draft.Patchsets[len(draft.Patchsets)-1].ChangedPaths
	if len(changed) != 1 || changed[0] != "/acme/payment/sym/new.txt" {
		t.Fatalf("the changeset should only add new.txt, got %v", changed)
	}
}
