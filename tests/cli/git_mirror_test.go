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
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
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
