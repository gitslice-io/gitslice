package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGitImportShallow(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/shallow",
		"--slice", "acme/payment",
		"--mode", "shallow",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			NativeCommitID string `json:"native_commit_id"`
			Message        string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 1 {
		t.Fatalf("shallow import commits = %d, want 1: %s", len(imported.Commits), raw)
	}
	if imported.Commits[0].Message != "third commit" {
		t.Fatalf("shallow import message = %q, want third commit", imported.Commits[0].Message)
	}
	inspect := runCLI(t, home, workspace, "show", imported.FinalCommitID)
	if !strings.Contains(inspect, "third commit") ||
		!strings.Contains(inspect, "/acme/payment/imported/shallow/README.md") {
		t.Fatalf("unexpected inspect output:\n%s", inspect)
	}
}

func TestGitImportDefaultsAuthoringSliceFromMount(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	// No --slice and no workspace: the authoring slice defaults to the mount
	// path's account home slice (acme:home), which covers /acme.
	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/imported/no-slice",
		"--mode", "shallow",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 1 {
		t.Fatalf("default-slice import commits = %d, want 1: %s", len(imported.Commits), raw)
	}
	inspect := runCLI(t, home, workspace, "show", imported.FinalCommitID)
	if !strings.Contains(inspect, "/acme/imported/no-slice/README.md") {
		t.Fatalf("unexpected inspect output:\n%s", inspect)
	}
}

func TestGitImportProgressText(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	stdout, stderr := runCLIStreams(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/progress",
		"--slice", "acme/payment",
		"--mode", "shallow",
	)
	if !strings.Contains(stdout, "imported 1 commit(s)") ||
		!strings.Contains(stdout, "final commit:") {
		t.Fatalf("unexpected import stdout:\n%s", stdout)
	}
	for _, want := range []string{"cloning repository", "found 1 commit(s)", "import complete"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("import stderr missing %q:\n%s", want, stderr)
		}
	}
}

func TestGitImportDeepListAndInspectCommits(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)

	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/deep",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			NativeCommitID string `json:"native_commit_id"`
			Message        string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 3 {
		t.Fatalf("deep import commits = %d, want 3: %s", len(imported.Commits), raw)
	}
	for i, want := range []string{"first commit", "second commit", "third commit"} {
		if imported.Commits[i].Message != want {
			t.Fatalf("commit %d message = %q, want %q", i, imported.Commits[i].Message, want)
		}
	}
	log := runCLI(t, home, workspace, "log", "--all", "--limit", "4")
	for _, want := range []string{"third commit", "second commit", "first commit"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "sha256:") {
		t.Fatalf("log should use short ids by default:\n%s", log)
	}
	secondShort := shortCommitPrefixForTest(t, imported.Commits[1].NativeCommitID)
	if !strings.Contains(log, secondShort+"  second commit") {
		t.Fatalf("log missing short id %s for second commit:\n%s", secondShort, log)
	}
	libList := runCLI(t, home, workspace, "log", "--all", "--path", "/acme/payment/imported/deep/lib")
	if !strings.Contains(libList, "second commit") || strings.Contains(libList, "first commit") || strings.Contains(libList, "third commit") {
		t.Fatalf("path-filtered lib history is wrong:\n%s", libList)
	}
	readmeList := runCLI(t, home, workspace, "log", "--all", "--", "/acme/payment/imported/deep/README.md")
	if !strings.Contains(readmeList, "first commit") || !strings.Contains(readmeList, "third commit") || strings.Contains(readmeList, "second commit") {
		t.Fatalf("path-filtered README history is wrong:\n%s", readmeList)
	}
	firstPageRaw := runCLI(t, home, workspace, "log", "--all", "--limit", "1", "--json", "--", "/acme/payment/imported/deep/README.md")
	var firstPage struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(firstPageRaw), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Commits) != 1 || firstPage.Commits[0].Message != "third commit" || firstPage.NextPageToken == "" {
		t.Fatalf("unexpected first commit page: %#v raw=%s", firstPage, firstPageRaw)
	}
	secondPageRaw := runCLI(t, home, workspace, "log", "--all", "--limit", "1", "--page-token", firstPage.NextPageToken, "--json", "--", "/acme/payment/imported/deep/README.md")
	var secondPage struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(secondPageRaw), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Commits) != 1 || secondPage.Commits[0].Message != "first commit" {
		t.Fatalf("unexpected second commit page: %#v raw=%s", secondPage, secondPageRaw)
	}
	if secondPage.NextPageToken != "" {
		thirdPageRaw := runCLI(t, home, workspace, "log", "--all", "--limit", "1", "--page-token", secondPage.NextPageToken, "--json", "--", "/acme/payment/imported/deep/README.md")
		var thirdPage struct {
			Commits []struct {
				Message string `json:"message"`
			} `json:"commits"`
			NextPageToken string `json:"next_page_token"`
		}
		if err := json.Unmarshal([]byte(thirdPageRaw), &thirdPage); err != nil {
			t.Fatal(err)
		}
		if len(thirdPage.Commits) != 1 || thirdPage.Commits[0].Message != "bootstrap acme test slices" || thirdPage.NextPageToken != "" {
			t.Fatalf("unexpected third commit page: %#v raw=%s", thirdPage, thirdPageRaw)
		}
	}
	show := runCLI(t, home, workspace, "show", secondShort)
	if !strings.Contains(show, "commit "+secondShort) ||
		!strings.Contains(show, "second commit") ||
		!strings.Contains(show, "/acme/payment/imported/deep/lib/code.go") {
		t.Fatalf("unexpected show output for short id %s:\n%s", secondShort, show)
	}
	showRaw := runCLI(t, home, workspace, "show", secondShort, "--json")
	var shown struct {
		ID      string `json:"id"`
		ShortID string `json:"short_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(showRaw), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.ID != imported.Commits[1].NativeCommitID || shown.ShortID != secondShort || shown.Message != "second commit" {
		t.Fatalf("unexpected show json: %#v raw=%s", shown, showRaw)
	}
	diffNameOnly := runCLI(t, home, workspace, "diff", secondShort, "--name-only")
	if !strings.Contains(diffNameOnly, "/acme/payment/imported/deep/lib/code.go") {
		t.Fatalf("commit diff --name-only missing changed path:\n%s", diffNameOnly)
	}
	cloneDir := cloneSlice(t, ts, token)
	assertProjectedFile(t, cloneDir, "acme/payment/imported/deep/README.md", "hello v2\n")
	assertProjectedFile(t, cloneDir, "acme/payment/imported/deep/lib/code.go", "package lib\nconst Value = 1\n")
}

func TestGitImportDeepMaxCommitsAndResume(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/bounded",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--max-commits", "2",
		"--json",
	)
	var imported struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 2 {
		t.Fatalf("bounded deep import commits = %d, want 2: %s", len(imported.Commits), raw)
	}
	for i, want := range []string{"second commit", "third commit"} {
		if imported.Commits[i].Message != want {
			t.Fatalf("commit %d message = %q, want %q", i, imported.Commits[i].Message, want)
		}
	}

	stdout, stderr := runCLIStreams(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/bounded",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--max-commits", "2",
	)
	if !strings.Contains(stdout, "imported 2 commit(s)") {
		t.Fatalf("unexpected resume stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "skipped") {
		t.Fatalf("resume stderr missing skipped progress:\n%s", stderr)
	}
}
