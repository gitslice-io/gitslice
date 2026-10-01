package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitProjectionKeepsImportedAuthorship checks that commits published by a
// Git import project with their original author, author date and full
// message, plus a Git-Commit trailer naming the original commit.
func TestGitProjectionKeepsImportedAuthorship(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)

	repo := t.TempDir()
	gitIn := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitIn(nil, "init", "-q", "-b", "main")
	commit := func(name, email, date, file, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, file), []byte(file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(nil, "add", file)
		gitIn([]string{
			"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
			"GIT_COMMITTER_NAME=C", "GIT_COMMITTER_EMAIL=c@example.invalid", "GIT_COMMITTER_DATE=" + date,
		}, "commit", "-q", "-m", message)
		return gitIn(nil, "rev-parse", "HEAD")
	}
	first := commit("Ada Lovelace", "ada@example.invalid", "2025-12-10T09:30:00+01:00", "a.txt", "Add a\n\nWhy a exists.")
	second := commit("Grace Hopper", "grace@example.invalid", "2026-01-02T15:04:05-08:00", "b.txt", "Add b")

	raw := runCLI(t, home, workspace, "import", repo, "--mount", "/acme/payment/vendor/lib", "--slice", "acme/payment", "--mode", "deep", "--json")
	var imported struct {
		Commits []json.RawMessage `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil || len(imported.Commits) != 2 {
		t.Fatalf("import output %q: %v", raw, err)
	}

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitWithAuth(t, token, "", "clone", "http://"+ts.gitAddr+"/git/acme/payment.git", cloneDir)
	log := runGit(t, cloneDir, "log", "--format=%an <%ae> %at|%cn%n%B%n--")
	for _, want := range []string{
		"Grace Hopper <grace@example.invalid> 1767395045|acme",
		"Ada Lovelace <ada@example.invalid> 1765355400|acme",
		"Add a\n\nWhy a exists.\n\nGit-Commit: " + first + "\nGitslice-Commit: ",
		"Add b\n\nGit-Commit: " + second + "\nGitslice-Commit: ",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("projected log missing %q:\n%s", want, log)
		}
	}
}
