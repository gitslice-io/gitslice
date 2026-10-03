package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGitPartialClone clones a slice without its file contents
// (--filter=blob:none) and then reads one file, which the client fetches from
// the server on demand. Agents and sandboxes use this to start work on a large
// slice without downloading all of it.
func TestGitPartialClone(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "partial.go", "package payment\nconst Partial = true\n")
	writeWorkspaceFile(t, workspace, "other.go", "package payment\nconst Other = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "partial clone target")
	runCLI(t, home, workspace, "cs", "submit")

	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	cloneDir := filepath.Join(t.TempDir(), "payment")
	header := "http.extraHeader=Authorization: Bearer " + token
	_, stderr, err := runGitResult("", "-c", header, "clone", "--filter=blob:none", "--no-checkout", gitURL, cloneDir)
	if err != nil {
		t.Fatalf("partial clone failed: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "filtering not recognized") {
		t.Fatalf("server ignored the filter:\n%s", stderr)
	}
	if got := runGit(t, "", "-C", cloneDir, "config", "remote.origin.partialclonefilter"); strings.TrimSpace(got) != "blob:none" {
		t.Fatalf("clone is not partial, partialclonefilter = %q", got)
	}
	// The blob is not in the clone yet: reading it fetches it from the server.
	missing := runGit(t, "", "-C", cloneDir, "rev-list", "--objects", "--missing=print", "origin/main")
	if !strings.Contains(missing, "?") {
		t.Fatalf("expected promised blobs after a blobless clone, got:\n%s", missing)
	}
	content := runGit(t, "", "-C", cloneDir, "-c", header, "cat-file", "-p", "origin/main:acme/payment/partial.go")
	if content != "package payment\nconst Partial = true\n" {
		t.Fatalf("lazily fetched blob has wrong contents:\n%s", content)
	}
}
