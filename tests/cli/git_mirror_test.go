package cli_test

import (
	"bytes"
	"log/slog"
	"os"
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

// captureLogs sends the in-process server's logs to l until the returned
// function is called.
func captureLogs(l *logBuffer) func() {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	return func() { slog.SetDefault(previous) }
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

// forgetProjection removes the instance's Git cache, as a new instance would
// not have one. The mirror in the object store stays.
func forgetProjection(t *testing.T, ts *testServer) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(ts.objectRoot, "git-cache")); err != nil {
		t.Fatal(err)
	}
}

// TestGitPackMirrorColdStart is the large-slice path end to end: the projection
// is built lazily, its history packs are copied to the object store, and a new
// instance with an empty cache restores them, adds only what landed since, and
// serves a full clone by writing the files it needs from the object store.
func TestGitPackMirrorColdStart(t *testing.T) {
	logs := &logBuffer{}
	t.Cleanup(captureLogs(logs))
	ts := startTestServer(t)

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
	forgetProjection(t, ts)
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

// A small eager import used to be written as loose objects, which belong to no
// pack, so a mirror made after the second change lacked the new head and a
// restore failed. Every change must reach the packs the mirror copies.
func TestGitPackMirrorCoversEagerIncrementalUpdates(t *testing.T) {
	logs := &logBuffer{}
	t.Cleanup(captureLogs(logs))
	ts := startTestServer(t)

	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	submitWorkspaceFile(t, home, workspace, "one.go", "package payment\nconst One = 1\n", "add one.go")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	header := "http.extraHeader=Authorization: Bearer " + token
	runGit(t, "", "-c", header, "clone", gitURL, filepath.Join(t.TempDir(), "first"))
	// One small change on top of a built cache.
	submitWorkspaceFile(t, home, workspace, "two.go", "package payment\nconst Two = 2\n", "add two.go")
	runGit(t, "", "-c", header, "clone", gitURL, filepath.Join(t.TempDir(), "second"))
	head := strings.Fields(runGit(t, "", "-c", header, "ls-remote", gitURL, "refs/heads/main"))[0]
	manifest := filepath.Join(ts.objectRoot, "git-mirror", "acme", "payment", "manifest.json")
	waitFor(t, "the pack copy of the second change", func() bool {
		raw, err := os.ReadFile(manifest)
		return err == nil && strings.Contains(string(raw), head)
	})

	ts.stop(t)
	forgetProjection(t, ts)
	ts.start(t, false)
	cold := filepath.Join(t.TempDir(), "cold")
	runGit(t, "", "-c", header, "clone", gitURL, cold)
	runGit(t, "", "-C", cold, "fsck", "--full")
	out := logs.String()
	if !strings.Contains(out, "git projection restored from mirror") || strings.Contains(out, "git mirror restore failed") {
		t.Fatalf("the new instance did not restore from the pack copy:\n%s", out)
	}
}
