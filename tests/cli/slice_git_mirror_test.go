package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mirrorStatus struct {
	URL              string `json:"url"`
	Root             string `json:"root"`
	Pending          bool   `json:"pending"`
	LastPushedCommit string `json:"last_pushed_commit"`
	LastError        string `json:"last_error"`
}

func readMirrorStatus(t *testing.T, home, dir, slice string) mirrorStatus {
	t.Helper()
	var status mirrorStatus
	if err := json.Unmarshal([]byte(runCLI(t, home, dir, "slice", "mirror", slice, "--json")), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

// waitForMirror waits until the mirror has pushed (or failed) and nothing is
// pending.
func waitForMirror(t *testing.T, home, dir, slice string, done func(mirrorStatus) bool) mirrorStatus {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		status := readMirrorStatus(t, home, dir, slice)
		if done(status) {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("mirror did not settle: %+v", status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestSliceGitMirror covers design/35: an owner sets a mirror, the server
// pushes the slice's history with its folder as the root, then every landing
// and tag, and stops (with the error shown) when the remote diverges.
func TestSliceGitMirror(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")
	submitWorkspaceFile(t, home, payment, "one.go", "package payment\nconst One = 1\n", "payment one")

	remote := filepath.Join(t.TempDir(), "github.git")
	runGit(t, "", "init", "-q", "--bare", "-b", "main", remote)

	if _, stderr := runCLIFails(t, home, payment, "slice", "mirror", "acme/payment"); !strings.Contains(stderr, "no Git mirror") {
		t.Fatalf("show without a mirror:\n%s", stderr)
	}
	if _, stderr := runCLIFails(t, home, payment, "slice", "mirror", "set", "acme/payment", "--url", remote, "--token-secret", "MIRROR_TOKEN"); !strings.Contains(stderr, "MIRROR_TOKEN is not set") || !strings.Contains(stderr, "gs slice secret set acme/payment MIRROR_TOKEN") {
		t.Fatalf("a missing token secret should be refused:\n%s", stderr)
	}
	runCLIStreamsWithInput(t, home, payment, "tok3n", "slice", "secret", "set", "acme/payment", "MIRROR_TOKEN")
	out := runCLI(t, home, payment, "slice", "mirror", "set", "acme/payment", "--url", remote, "--token-secret", "MIRROR_TOKEN")
	if !strings.Contains(out, "root:    /acme/payment becomes the repository root") {
		t.Fatalf("mirror set output:\n%s", out)
	}
	status := waitForMirror(t, home, payment, "acme/payment", func(s mirrorStatus) bool { return !s.Pending && (s.LastPushedCommit != "" || s.LastError != "") })
	if status.LastError != "" {
		t.Fatalf("first push failed: %s", status.LastError)
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-parse", "main")); got != status.LastPushedCommit {
		t.Fatalf("remote main %s, status says %s", got, status.LastPushedCommit)
	}
	if got := runGit(t, remote, "show", "main:one.go"); !strings.Contains(got, "const One = 1") {
		t.Fatalf("remote one.go:\n%s", got)
	}

	// A landing and a tag reach the remote by themselves.
	submitWorkspaceFile(t, home, payment, "two.go", "package payment\nconst Two = 2\n", "payment two")
	runCLI(t, home, payment, "tag", "create", "v1.0.0", "-m", "first release")
	waitForMirror(t, home, payment, "acme/payment", func(mirrorStatus) bool {
		out, _, err := runGitResult(remote, "log", "-1", "--format=%s", "v1.0.0")
		return err == nil && strings.TrimSpace(out) == "payment two"
	})
	if got := strings.TrimSpace(runGit(t, remote, "log", "--format=%s", "main")); got != "payment two\npayment one" {
		t.Fatalf("remote history:\n%s", got)
	}
	if tags := strings.TrimSpace(runGit(t, remote, "tag", "-l")); tags != "v1.0.0" {
		t.Fatalf("remote tags = %q (Go subdirectory copies must stay out)", tags)
	}

	// A commit made on the remote stops the mirror; the error says why.
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", "-q", remote, clone)
	if err := os.WriteFile(filepath.Join(clone, "rogue.txt"), []byte("rogue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "add", "-A")
	runGit(t, clone, "-c", "user.name=x", "-c", "user.email=x@example.invalid", "commit", "-q", "-m", "pushed on the remote")
	runGit(t, clone, "push", "-q", "origin", "HEAD:main")
	submitWorkspaceFile(t, home, payment, "three.go", "package payment\nconst Three = 3\n", "payment three")
	status = waitForMirror(t, home, payment, "acme/payment", func(s mirrorStatus) bool { return s.LastError != "" })
	if !strings.Contains(status.LastError, "did not come from Gitslice") {
		t.Fatalf("divergence error = %q", status.LastError)
	}

	runCLI(t, home, payment, "slice", "mirror", "rm", "acme/payment")
	if _, stderr := runCLIFails(t, home, payment, "slice", "mirror", "acme/payment"); !strings.Contains(stderr, "no Git mirror") {
		t.Fatalf("show after removal:\n%s", stderr)
	}
}
