package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitImportPreservesOriginalMetadata imports commits by different authors
// with multi-line messages and checks that the original author, author date
// and full message come back on the native commits.
func TestGitImportPreservesOriginalMetadata(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)

	repo := t.TempDir()
	gitIn := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitIn(nil, "init", "-q", "-b", "main")
	commit := func(name, email, date, file, message string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, file), []byte(file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(nil, "add", file)
		env := []string{
			"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
			"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.invalid", "GIT_COMMITTER_DATE=" + date,
		}
		gitIn(env, "commit", "-q", "-m", message)
	}
	commit("Ada Lovelace", "ada@example.invalid", "2025-12-10T09:30:00+01:00", "a.txt", "Add a\n\nExplains why a exists.\nCo-Authored-By: Grace <grace@example.invalid>")
	commit("Grace Hopper", "grace@example.invalid", "2026-01-02T15:04:05-08:00", "b.txt", "Add b")

	raw := runCLI(t, home, workspace,
		"import", repo,
		"--mount", "/acme/payment/imported/fidelity",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--json",
	)
	var imported struct {
		Commits []struct {
			GitCommitID    string `json:"git_commit_id"`
			NativeCommitID string `json:"native_commit_id"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatalf("import output %q: %v", raw, err)
	}
	if len(imported.Commits) != 2 {
		t.Fatalf("imported %d commits, want 2: %s", len(imported.Commits), raw)
	}

	var shown struct {
		Message   string `json:"message"`
		GitImport *struct {
			GitCommitID string `json:"git_commit_id"`
			AuthorName  string `json:"author_name"`
			AuthorEmail string `json:"author_email"`
			AuthoredAt  string `json:"authored_at"`
			Message     string `json:"message"`
		} `json:"git_import"`
	}
	showRaw := runCLI(t, home, workspace, "show", imported.Commits[0].NativeCommitID, "--json")
	if err := json.Unmarshal([]byte(showRaw), &shown); err != nil {
		t.Fatalf("show output %q: %v", showRaw, err)
	}
	got := shown.GitImport
	if got == nil {
		t.Fatalf("show is missing git_import:\n%s", showRaw)
	}
	if shown.Message != "Add a" ||
		got.GitCommitID != imported.Commits[0].GitCommitID ||
		got.AuthorName != "Ada Lovelace" || got.AuthorEmail != "ada@example.invalid" ||
		got.AuthoredAt != "2025-12-10T08:30:00Z" ||
		got.Message != "Add a\n\nExplains why a exists.\nCo-Authored-By: Grace <grace@example.invalid>" {
		t.Fatalf("unexpected imported metadata: message=%q %+v", shown.Message, *got)
	}

	text := runCLI(t, home, workspace, "show", imported.Commits[1].NativeCommitID)
	for _, want := range []string{"Imported: git ", "by Grace Hopper <grace@example.invalid>", "on 2026-01-02T23:04:05Z", "    Add b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("gs show text missing %q:\n%s", want, text)
		}
	}

	logRaw := runCLI(t, home, workspace, "log", "--all", "--limit", "5", "--json")
	if !strings.Contains(logRaw, `"author_name": "Grace Hopper"`) && !strings.Contains(logRaw, `"author_name":"Grace Hopper"`) {
		t.Fatalf("gs log --json is missing imported authors:\n%s", logRaw)
	}
}
