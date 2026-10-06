package cli_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// TestSubmitNamesTheChangesetLink checks that what gs prints after a submit
// leads people (and agents) to the changeset: its id and link come first, the
// commit id is labelled as one, and gs cs link only prints links that open.
func TestSubmitNamesTheChangesetLink(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspace, "link.go", "package payment\nconst Link = 1\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "link output")
	out := runCLI(t, home, workspace, "cs", "submit")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	first := regexp.MustCompile(`^submitted changeset ([0-9a-f]+) to refs/global/main$`).FindStringSubmatch(lines[0])
	if first == nil || len(lines) < 3 {
		t.Fatalf("submit output should start with the changeset:\n%s", out)
	}
	handle := first[1]
	if !strings.HasPrefix(lines[1], "view: ") || !strings.HasSuffix(lines[1], "/cs/"+handle) {
		t.Fatalf("second line should be the changeset link:\n%s", out)
	}
	if !strings.HasPrefix(lines[2], "landed as commit sha256:") || !strings.Contains(lines[2], "not a changeset id") {
		t.Fatalf("the commit id should be labelled:\n%s", out)
	}
	commit := strings.Fields(strings.TrimPrefix(lines[2], "landed as commit "))[0]

	// gs cs link checks the changeset and prints its link.
	if link := strings.TrimSpace(runCLI(t, home, workspace, "cs", "link", handle)); !strings.HasSuffix(link, "/cs/"+handle) {
		t.Fatalf("cs link = %q", link)
	}
	var linked struct {
		Changeset    string `json:"changeset"`
		ChangesetURL string `json:"changeset_url"`
		Title        string `json:"title"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "cs", "link", handle, "--json")), &linked); err != nil {
		t.Fatal(err)
	}
	if linked.Changeset != handle || !strings.HasSuffix(linked.ChangesetURL, "/cs/"+handle) || linked.Title != "link output" {
		t.Fatalf("cs link --json = %+v", linked)
	}
	// A commit id is refused, with a hint, rather than turned into a dead link.
	if _, err := runCLIResult(home, workspace, "cs", "link", commit); err == nil || !strings.Contains(err.Error(), "commit id") {
		t.Fatalf("cs link with a commit id should explain itself, got %v", err)
	}
	if _, err := runCLIResult(home, workspace, "cs", "link", "deadbeef00"); err == nil {
		t.Fatal("cs link with an unknown id should fail")
	}

	// The JSON a script reads names the changeset link explicitly.
	writeWorkspaceFile(t, workspace, "link.go", "package payment\nconst Link = 2\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "link output json")
	var submitted struct {
		Changeset    string `json:"changeset"`
		ChangesetURL string `json:"changeset_url"`
		CommitID     string `json:"commit_id"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "cs", "submit", "--json")), &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.Changeset == "" || !strings.HasSuffix(submitted.ChangesetURL, "/cs/"+submitted.Changeset) || !strings.HasPrefix(submitted.CommitID, "sha256:") {
		t.Fatalf("submit --json = %+v", submitted)
	}
}
