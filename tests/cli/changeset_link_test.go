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
	// A landed commit's id, full or shortened as gs prints it, leads to the
	// changeset that landed it, and says so.
	for _, id := range []string{commit, strings.TrimPrefix(commit, "sha256:")[:12]} {
		out, stderr := runCLIStreams(t, home, workspace, "cs", "link", id)
		if !strings.HasSuffix(strings.TrimSpace(out), "/cs/"+handle) || !strings.Contains(stderr, "is the commit changeset "+handle+" landed as") {
			t.Fatalf("cs link %s = %q (stderr %q), want the changeset %s", id, out, stderr, handle)
		}
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

// TestFileCommandsNameTheChangesetLink checks that gs fs writes, which create
// and land a changeset in one step, print that changeset and its link, not
// just the commit (an agent turned the commit id into a dead /cs/ link).
func TestFileCommandsNameTheChangesetLink(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	dir := t.TempDir()
	loginTestCLI(t, ts, home, dir)

	out := runCLI(t, home, dir, "fs", "write", "/acme/payment/fs-link.txt", "--text", "hello\n")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	first := regexp.MustCompile(`^wrote /acme/payment/fs-link\.txt in \S+ through changeset ([0-9a-f]+)$`).FindStringSubmatch(lines[0])
	if first == nil || len(lines) < 3 {
		t.Fatalf("fs write output should name the changeset:\n%s", out)
	}
	handle := first[1]
	if !strings.HasPrefix(lines[1], "view: ") || !strings.HasSuffix(lines[1], "/cs/"+handle) {
		t.Fatalf("second line should be the changeset link:\n%s", out)
	}
	if !strings.HasPrefix(lines[2], "landed as commit sha256:") || !strings.Contains(lines[2], "not a changeset id") {
		t.Fatalf("the commit id should be labelled:\n%s", out)
	}
	if link := strings.TrimSpace(runCLI(t, home, dir, "cs", "link", handle)); !strings.HasSuffix(link, "/cs/"+handle) {
		t.Fatalf("cs link %s = %q", handle, link)
	}

	var written struct {
		Changeset    string `json:"changeset"`
		ChangesetURL string `json:"changeset_url"`
		CommitID     string `json:"commit_id"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, dir, "fs", "write", "/acme/payment/fs-link.txt", "--text", "again\n", "--json")), &written); err != nil {
		t.Fatal(err)
	}
	if written.Changeset == "" || written.Changeset == handle || !strings.HasSuffix(written.ChangesetURL, "/cs/"+written.Changeset) || !strings.HasPrefix(written.CommitID, "sha256:") {
		t.Fatalf("fs write --json = %+v", written)
	}
}

// TestCLIShowsWhichIDsHaveAPage covers the rest of an agent's report on
// dead links: gs create prints the link, gs log names the changeset behind
// each commit, gs browse checks a changeset before printing its URL, and
// gs fs --no-submit leaves a changeset open for review.
func TestCLIShowsWhichIDsHaveAPage(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspace, "pages.go", "package payment\nconst Pages = 1\n")
	created := runCLI(t, home, workspace, "create", "--all", "--message", "pages")
	match := regexp.MustCompile(`(?m)^created ([0-9a-f]+) patchset 1\nview: (\S+)$`).FindStringSubmatch(created)
	if match == nil || !strings.HasSuffix(match[2], "/cs/"+match[1]) {
		t.Fatalf("cs create should print the changeset link:\n%s", created)
	}
	handle := match[1]
	commit := submittedRefCommitID(t, runCLI(t, home, workspace, "cs", "submit", "--json"))

	// gs log names the changeset next to each commit it landed.
	var log struct {
		Commits []struct {
			ID           string `json:"id"`
			Changeset    string `json:"changeset"`
			ChangesetURL string `json:"changeset_url"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "log", "--limit", "5", "--json")), &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Commits) == 0 || log.Commits[0].ID != commit || log.Commits[0].Changeset != handle || !strings.HasSuffix(log.Commits[0].ChangesetURL, "/cs/"+handle) {
		t.Fatalf("gs log --json first commit = %+v, want commit %s from changeset %s", log.Commits, commit, handle)
	}
	if text := runCLI(t, home, workspace, "log", "--limit", "1"); !strings.Contains(text, "(changeset "+handle+")") {
		t.Fatalf("gs log should name the changeset:\n%s", text)
	}

	// gs browse checks the id and links the changeset, even from a commit id.
	short := strings.TrimPrefix(commit, "sha256:")[:12]
	out, stderr := runCLIStreams(t, home, workspace, "browse", "--print", "cs/"+short)
	if !strings.HasSuffix(strings.TrimSpace(out), "/cs/"+handle) || !strings.Contains(stderr, "landed as") {
		t.Fatalf("browse cs/<commit> = %q (stderr %q), want the changeset %s", out, stderr, handle)
	}
	if _, stderr := runCLIFails(t, home, workspace, "browse", "--print", "cs/deadbeef00"); !strings.Contains(stderr, "no changeset") {
		t.Fatalf("browse with a bogus id should fail:\n%s", stderr)
	}

	// gs fs --no-submit leaves the changeset open for review.
	draft := runCLI(t, home, workspace, "fs", "write", "/acme/payment/review.txt", "--text", "please review\n", "--no-submit")
	open := regexp.MustCompile(`changeset ([0-9a-f]+) is open for review, not submitted`).FindStringSubmatch(draft)
	if open == nil || !strings.Contains(draft, "view: ") || !strings.Contains(draft, "gs cs submit "+open[1]) {
		t.Fatalf("fs write --no-submit output:\n%s", draft)
	}
	var shown struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "cs", "show", open[1], "--json")), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Status == "submitted" {
		t.Fatalf("--no-submit changeset was submitted")
	}
	runCLI(t, home, workspace, "cs", "submit", open[1])
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "cs", "show", open[1], "--json")), &shown); err != nil || shown.Status != "submitted" {
		t.Fatalf("after gs cs submit: %+v, %v", shown, err)
	}
}
