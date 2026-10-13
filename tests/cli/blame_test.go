package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBlameFollowsLinesAndMoves lands four changes to a file, the third a
// move, and checks gs blame credits each line to the change that last
// touched it, with the changeset that landed it.
func TestBlameFollowsLinesAndMoves(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	land := func(message string) {
		t.Helper()
		if out := runCLI(t, home, workspace, "create", "--all", "--message", message, "--submit"); !strings.Contains(out, "submitted ") {
			t.Fatalf("%s:\n%s", message, out)
		}
	}
	writeWorkspaceFile(t, workspace, "blame.go", "package payment\nconst A = 1\nconst B = 1\n")
	land("add blame.go")
	writeWorkspaceFile(t, workspace, "blame.go", "package payment\nconst A = 2\nconst B = 1\nconst C = 2\n")
	land("change A, add C")
	data, err := os.ReadFile(filepath.Join(workspace, "blame.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workspace, "blame.go")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, workspace, "renamed.go", string(data))
	land("move to renamed.go")
	writeWorkspaceFile(t, workspace, "renamed.go", "package payment // moved\nconst A = 2\nconst B = 1\nconst C = 2\n")
	land("comment the package")

	var blame struct {
		Path      string `json:"path"`
		LineCount int    `json:"line_count"`
		Truncated bool   `json:"truncated"`
		Ranges    []struct {
			StartLine int    `json:"start_line"`
			LineCount int    `json:"line_count"`
			CommitID  string `json:"commit_id"`
		} `json:"ranges"`
		Commits []struct {
			ID           string `json:"id"`
			Summary      string `json:"summary"`
			ChangesetID  string `json:"changeset_id"`
			ChangesetURL string `json:"changeset_url"`
		} `json:"commits"`
	}
	out := runCLI(t, home, workspace, "blame", "renamed.go", "--json")
	if err := json.Unmarshal([]byte(out), &blame); err != nil {
		t.Fatalf("blame --json: %v\n%s", err, out)
	}
	summaries := map[string]string{}
	for _, c := range blame.Commits {
		summaries[c.ID] = c.Summary
		if !strings.HasPrefix(c.ChangesetID, "cs_") || !strings.Contains(c.ChangesetURL, "/cs/") {
			t.Fatalf("commit without its changeset: %+v", c)
		}
	}
	perLine := []string{}
	for _, rg := range blame.Ranges {
		for i := 0; i < rg.LineCount; i++ {
			perLine = append(perLine, summaries[rg.CommitID])
		}
	}
	want := []string{"comment the package", "change A, add C", "add blame.go", "change A, add C"}
	if blame.Path != "/acme/payment/renamed.go" || blame.LineCount != 4 || blame.Truncated || strings.Join(perLine, "|") != strings.Join(want, "|") {
		t.Fatalf("blame = %+v\nper line %q, want %q", blame, perLine, want)
	}

	text := runCLI(t, home, workspace, "blame", "renamed.go", "--lines", "2,3")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " 2) const A = 2") || !strings.HasSuffix(lines[1], " 3) const B = 1") {
		t.Fatalf("blame text:\n%s", text)
	}
	if _, stderr := runCLIFails(t, home, workspace, "blame", "missing.go"); !strings.Contains(stderr, "no file at /acme/payment/missing.go") {
		t.Fatalf("missing file: %s", stderr)
	}
}
