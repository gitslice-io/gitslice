package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A message of several lines is a title and a description, not a long title;
// --description sets one directly, gs cs edit changes either while the
// changeset is open, and both are the commit's message once it lands.
func TestChangesetTitleAndDescriptionThroughTheCLI(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	// A whole message sent as --message is split.
	writeWorkspaceFile(t, workspace, "described/one.go", "package described\nconst One = 1\n")
	out := runCLI(t, home, workspace, "create", "--all", "--message", "Add the one constant\n\nIt is the first of several.\nSee the design doc.", "--json")
	var created struct {
		ChangesetID string `json:"changeset_id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("create --json: %v\n%s", err, out)
	}
	status := runCLI(t, home, workspace, "cs", "show")
	for _, want := range []string{"title: Add the one constant\n", "description:\n  It is the first of several.\n  See the design doc.\n"} {
		if !strings.Contains(status, want) {
			t.Fatalf("gs cs show is missing %q:\n%s", want, status)
		}
	}
	if strings.Contains(status, "title: Add the one constant\n\n") || strings.Contains(status, "title: Add the one constant It") {
		t.Fatalf("the title still carries the body:\n%s", status)
	}

	// Edit the description from a file, and the title.
	descriptionFile := filepath.Join(t.TempDir(), "description.md")
	if err := os.WriteFile(descriptionFile, []byte("Adds `One`.\n\n- first\n- second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, workspace, "cs", "edit", "--description-file", descriptionFile, "--title", "Add One")
	var shown struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "cs", "status", "--json")), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Title != "Add One" || shown.Description != "Adds `One`.\n\n- first\n- second" {
		t.Fatalf("after cs edit: %+v", shown)
	}
	if _, stderr := runCLIFails(t, home, workspace, "cs", "edit"); !strings.Contains(stderr, "nothing to change") {
		t.Fatalf("cs edit with nothing to change: %s", stderr)
	}
	if out, err := runCLIResult(home, workspace, "cs", "edit", "--description", "x", "--description-file", descriptionFile); err == nil {
		t.Fatalf("both --description and --description-file were accepted:\n%s", out)
	}

	// Land it: the commit's message is the title, then the description.
	var submitted struct {
		Results []struct {
			CommitID string `json:"commit_id"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "submit", "--json")), &submitted); err != nil || len(submitted.Results) != 1 {
		t.Fatalf("submit --json: %v %+v", err, submitted)
	}
	var commit struct {
		Message string `json:"message"`
	}
	// The commit is readable shortly after submit returns, not at once.
	var shownRaw string
	for attempt := 0; ; attempt++ {
		out, err := runCLIResult(home, workspace, "show", submitted.Results[0].CommitID, "--json")
		if err == nil {
			shownRaw = out
			break
		}
		if attempt >= 50 {
			t.Fatalf("gs show never found the landed commit: %v\n%s", err, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := json.Unmarshal([]byte(shownRaw), &commit); err != nil {
		t.Fatal(err)
	}
	if want := "Add One\n\nAdds `One`.\n\n- first\n- second"; strings.TrimSpace(commit.Message) != want {
		t.Fatalf("the landed commit's message = %q, want %q", commit.Message, want)
	}

	// A landed changeset's text is its commit's: no more edits.
	if _, stderr := runCLIFails(t, home, workspace, "cs", "edit", created.ChangesetID, "--title", "Rewritten"); !strings.Contains(stderr, "while it is open") {
		t.Fatalf("editing a landed changeset: %s", stderr)
	}

	// --description sets it directly.
	writeWorkspaceFile(t, workspace, "described/two.go", "package described\nconst Two = 2\n")
	runCLI(t, home, workspace, "create", "--all", "--message", "Add Two", "--description", "The second constant.")
	if shown2 := runCLI(t, home, workspace, "cs", "show"); !strings.Contains(shown2, "description:\n  The second constant.\n") {
		t.Fatalf("--description was not kept:\n%s", shown2)
	}
}
