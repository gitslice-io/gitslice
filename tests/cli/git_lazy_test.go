package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitLazyProjection runs a slice through the lazy projection: history is
// built from recorded Git blob ids without reading files, and a file's contents
// are written into the server's repository only when a fetch needs them.
func TestGitLazyProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	files := map[string]string{
		"main.go":              "package payment\n\nfunc Main() {}\n",
		"util/strings.go":      "package util\n\nconst Name = \"payment\"\n",
		"util/deep/nested.txt": "nested file\n",
		"docs/README.md":       "# Payment\n",
	}
	for path, content := range files {
		writeWorkspaceFile(t, workspace, path, content)
		runCLI(t, home, workspace, "cs", "create", "--title", "add "+path)
		runCLI(t, home, workspace, "cs", "submit")
	}
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	header := "http.extraHeader=Authorization: Bearer " + token

	// What the first instance serves.
	firstClone := filepath.Join(t.TempDir(), "first")
	runGit(t, "", "-c", header, "clone", gitURL, firstClone)
	firstHead := strings.TrimSpace(runGit(t, "", "-C", firstClone, "rev-parse", "HEAD"))
	firstLog := runGit(t, "", "-C", firstClone, "log", "--format=%H %T")

	// A new instance with neither a cache nor a mirror to restore builds the
	// history again from nothing, and must arrive at the same commits.
	ts.stop(t)
	forgetProjection(t, ts)
	if err := os.RemoveAll(filepath.Join(ts.objectRoot, "git-mirror")); err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	restore := captureLogs(logs)
	defer restore()
	ts.start(t, false)

	// 1. A blobless clone needs no file contents, so the server writes none.
	blobless := filepath.Join(t.TempDir(), "blobless")
	runGit(t, "", "-c", header, "clone", "--filter=blob:none", "--no-checkout", gitURL, blobless)
	if got := strings.TrimSpace(runGit(t, "", "-C", blobless, "rev-parse", "HEAD")); got != firstHead {
		t.Fatalf("lazy head %s, eager head %s: the projections diverged", got, firstHead)
	}
	if got := runGit(t, "", "-C", blobless, "log", "--format=%H %T"); got != firstLog {
		t.Fatalf("lazy history differs from eager:\n%s\nvs\n%s", got, firstLog)
	}
	cache := filepath.Join(ts.objectRoot, "git-cache", "acme", "payment.git")
	mainID := strings.TrimSpace(runGitDir(t, cache, "rev-parse", firstHead+":acme/payment/main.go"))
	if err := exec.Command("git", "-C", cache, "cat-file", "-e", mainID).Run(); err == nil {
		t.Fatal("the server wrote a file's contents although nothing asked for them")
	}
	if !strings.Contains(logs.String(), "git projection built lazily") {
		t.Fatalf("the history was not built lazily:\n%s", logs.String())
	}

	// 2. Reading one file lazily asks the server for just that file.
	if got := runGit(t, "", "-C", blobless, "-c", header, "cat-file", "-p", "HEAD:acme/payment/util/strings.go"); got != files["util/strings.go"] {
		t.Fatalf("lazily fetched file = %q", got)
	}
	if err := exec.Command("git", "-C", cache, "cat-file", "-e", mainID).Run(); err == nil {
		t.Fatal("fetching one file hydrated another")
	}

	// 3. A full clone needs everything: the server hydrates it first.
	full := filepath.Join(t.TempDir(), "full")
	runGit(t, "", "-c", header, "clone", gitURL, full)
	for path, want := range files {
		got, err := os.ReadFile(filepath.Join(full, "acme", "payment", filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Fatalf("%s in the full clone = %q (%v), want %q", path, got, err, want)
		}
	}
	runGit(t, "", "-C", full, "fsck", "--full")

	// 4. A shallow clone gets one commit's worth of files.
	shallow := filepath.Join(t.TempDir(), "shallow")
	runGit(t, "", "-c", header, "clone", "--depth", "1", gitURL, shallow)
	if got, _ := os.ReadFile(filepath.Join(shallow, "acme", "payment", "main.go")); string(got) != files["main.go"] {
		t.Fatalf("shallow clone main.go = %q", got)
	}

	// 5. New work lands on the lazy projection and reaches an existing clone.
	writeWorkspaceFile(t, workspace, "added.go", "package payment\n\nconst Added = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "added after the switch")
	runCLI(t, home, workspace, "cs", "submit")
	runGit(t, "", "-C", full, "-c", header, "pull", "--ff-only")
	if got, _ := os.ReadFile(filepath.Join(full, "acme", "payment", "added.go")); string(got) != "package payment\n\nconst Added = true\n" {
		t.Fatalf("pulled added.go = %q", got)
	}

	// 6. A push into a changeset works on a repository without all its files.
	if err := os.WriteFile(filepath.Join(full, "acme", "payment", "pushed.go"), []byte("package payment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "-C", full, "add", "-A")
	runGit(t, "", "-C", full, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-m", "push from a lazy clone")
	_, stderr, err := runGitResult(full, "-c", header, "push", "origin", "HEAD:refs/changes/new")
	if err != nil || !strings.Contains(stderr, "Created changeset") {
		t.Fatalf("push into a changeset from a lazy clone: %v\n%s", err, stderr)
	}
}

func runGitDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// TestGitLazyMergesHistoryPacks lands many commits one at a time, as a busy
// slice does, and checks that the server does not keep a pack per commit.
func TestGitLazyMergesHistoryPacks(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	header := "http.extraHeader=Authorization: Bearer " + token
	for i := 0; i < 24; i++ {
		name := "f" + strings.Repeat("x", i) + ".txt"
		submitWorkspaceFile(t, home, workspace, name, "file "+name+"\n", "land "+name)
		runGit(t, "", "-c", header, "ls-remote", gitURL) // each landing is projected on its own
	}
	cache := filepath.Join(ts.objectRoot, "git-cache", "acme", "payment.git")
	packs, err := filepath.Glob(filepath.Join(cache, "objects", "pack", "pack-*.pack"))
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) == 0 || len(packs) >= 16 {
		t.Fatalf("%d packs after 24 landings, want a handful", len(packs))
	}
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "-c", header, "clone", gitURL, clone)
	runGit(t, "", "-C", clone, "fsck", "--full")
	if got := strings.Count(runGit(t, "", "-C", clone, "log", "--format=%H"), "\n"); got != 24 {
		t.Fatalf("clone has %d commits, want 24", got)
	}
}
