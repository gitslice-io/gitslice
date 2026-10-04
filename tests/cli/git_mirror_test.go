package cli_test

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// logBuffer collects the in-process server's log lines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// TestGitMirrorColdStart simulates a new server instance: the Git projection
// cache is gone, but the slice was mirrored, so the instance restores it and
// then extends it with what landed since instead of rebuilding from scratch.
func TestGitMirrorColdStart(t *testing.T) {
	logs := &logBuffer{}
	t.Cleanup(captureLogs(logs))
	ts := startTestServer(t)
	mirror := t.TempDir()
	ts.mirrorDir = mirror
	ts.mirrorSlices = "acme/payment"
	ts.restart(t)

	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	submitWorkspaceFile(t, home, workspace, "first.go", "package payment\nconst First = 1\n", "first")
	submitWorkspaceFile(t, home, workspace, "second.go", "package payment\nconst Second = 2\n", "second")

	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	header := "http.extraHeader=Authorization: Bearer " + token
	runGit(t, "", "-c", header, "clone", gitURL, filepath.Join(t.TempDir(), "warm"))
	before := runGit(t, "", "-c", header, "ls-remote", gitURL, "refs/heads/main")

	// The projection is pushed to the mirror in the background.
	mirrored := filepath.Join(mirror, "acme", "payment.git")
	waitFor(t, "the projection to reach the mirror", func() bool {
		out, err := gitOutputForTest(mirrored, "rev-parse", "--verify", "-q", "refs/heads/gitslice-state")
		return err == nil && strings.TrimSpace(out) != ""
	})

	// A new instance: nothing cached.
	ts.stop(t)
	if err := os.RemoveAll(filepath.Join(ts.objectRoot, "git-cache")); err != nil {
		t.Fatal(err)
	}
	ts.start(t, false)

	// Something lands while no cache exists; the restored instance extends it
	// rather than replaying the history.
	submitWorkspaceFile(t, home, workspace, "third.go", "package payment\nconst Third = 3\n", "third")
	cold := filepath.Join(t.TempDir(), "cold")
	runGit(t, "", "-c", header, "clone", gitURL, cold)
	for name, want := range map[string]string{
		"first.go":  "package payment\nconst First = 1\n",
		"second.go": "package payment\nconst Second = 2\n",
		"third.go":  "package payment\nconst Third = 3\n",
	} {
		got, err := os.ReadFile(filepath.Join(cold, "acme", "payment", name))
		if err != nil || string(got) != want {
			t.Fatalf("%s after a cold start = %q (%v), want %q", name, got, err, want)
		}
	}
	if out := logs.String(); !strings.Contains(out, "git projection restored from mirror") {
		t.Fatalf("the new instance did not restore from the mirror:\n%s", out)
	}
	if out := logs.String(); strings.Contains(out, "git mirror restore failed") {
		t.Fatalf("restoring from the mirror failed:\n%s", out)
	}
	// Commit ids are the same on every instance, restored or built.
	log := runGit(t, "", "-C", cold, "log", "--format=%H")
	if !strings.Contains(log, strings.Fields(before)[0]) {
		t.Fatalf("the restored history lost commit %s that the first instance served:\n%s", strings.Fields(before)[0], log)
	}
}

// captureLogs sends the in-process server's logs to l until the returned
// function is called.
func captureLogs(l *logBuffer) func() {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	return func() { slog.SetDefault(previous) }
}

func gitOutputForTest(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestGitPackMirrorColdStart is the large-slice path end to end: the projection
// is built lazily, its history packs are copied to the object store, and a new
// instance with an empty cache restores them, adds only what landed since, and
// serves a full clone by writing the files it needs from the object store.
func TestGitPackMirrorColdStart(t *testing.T) {
	logs := &logBuffer{}
	t.Cleanup(captureLogs(logs))
	ts := startTestServer(t)
	ts.lazy = true
	ts.mirrorPacks = true
	ts.mirrorSlices = "acme/payment"
	ts.restart(t)

	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	files := map[string]string{
		"one.go":      "package payment\nconst One = 1\n",
		"dir/two.go":  "package payment\nconst Two = 2\n",
		"dir/three.t": "three\n",
		"four.go":     "package payment\nconst Four = 4\n",
	}
	for _, name := range []string{"one.go", "dir/two.go", "dir/three.t"} {
		submitWorkspaceFile(t, home, workspace, name, files[name], "add "+name)
	}
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	header := "http.extraHeader=Authorization: Bearer " + token
	runGit(t, "", "-c", header, "clone", gitURL, filepath.Join(t.TempDir(), "warm"))
	head := strings.Fields(runGit(t, "", "-c", header, "ls-remote", gitURL, "refs/heads/main"))[0]

	// The history packs and their manifest reach the object store.
	manifest := filepath.Join(ts.objectRoot, "git-mirror", "acme", "payment", "manifest.json")
	waitFor(t, "the pack copy of the history", func() bool {
		raw, err := os.ReadFile(manifest)
		return err == nil && strings.Contains(string(raw), head)
	})

	// A new instance: nothing cached.
	ts.stop(t)
	if err := os.RemoveAll(filepath.Join(ts.objectRoot, "git-cache")); err != nil {
		t.Fatal(err)
	}
	ts.start(t, false)
	submitWorkspaceFile(t, home, workspace, "four.go", files["four.go"], "add four.go")
	cold := filepath.Join(t.TempDir(), "cold")
	runGit(t, "", "-c", header, "clone", gitURL, cold)
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(cold, "acme", "payment", filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Fatalf("%s after a cold start = %q (%v), want %q", name, got, err, want)
		}
	}
	runGit(t, "", "-C", cold, "fsck", "--full")
	out := logs.String()
	if !strings.Contains(out, "git projection restored from mirror") || strings.Contains(out, "git mirror restore failed") {
		t.Fatalf("the new instance did not restore from the pack copy:\n%s", out)
	}
	// Only the commit that landed after the restore was projected.
	if !strings.Contains(out, "git projection built lazily") || !strings.Contains(out, "commits=1 ") {
		t.Fatalf("expected a lazy build of just the new commit after the restore:\n%s", out)
	}
}
