package cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitFetchWithLargeNegotiation fetches a slice into a repository with a lot
// of unrelated history. Git then sends a large negotiation and gzips it, which
// http-backend only accepts when the request's Content-Encoding reaches it.
func TestGitFetchWithLargeNegotiation(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	submitWorkspaceFile(t, home, workspace, "fetch.go", "package payment\nconst Fetch = 1\n", "fetch target")

	local := t.TempDir()
	runGit(t, local, "init", "-q")
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.invalid")
	for i := 0; i < 120; i++ {
		if err := os.WriteFile(filepath.Join(local, "n.txt"), []byte(fmt.Sprintf("%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-am", fmt.Sprintf("local %d", i))
		if i == 0 {
			cmd = exec.Command("sh", "-c", "git add n.txt && git commit -q -m 'local 0'")
		}
		cmd.Dir = local
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("local commit %d: %v\n%s", i, err, out)
		}
	}
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	_, stderr, err := runGitResult(local, append(gitAuth(token), "-c", "http.postBuffer=1048576", "fetch", gitURL, "+refs/heads/main:refs/remotes/slice/main")...)
	if err != nil {
		t.Fatalf("fetch with a large negotiation failed: %v\n%s", err, stderr)
	}
	if got := strings.TrimSpace(runGit(t, local, "log", "-1", "--format=%s", "refs/remotes/slice/main")); got != "fetch target" {
		t.Fatalf("fetched head = %q", got)
	}
}
