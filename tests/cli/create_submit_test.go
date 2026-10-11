package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCreateSubmitLandsInOneStep checks gs create --submit: it lands a
// changeset whose slice needs nothing more, and when a required check never
// reports, it waits, gives up at --watch-timeout, and leaves the changeset
// created.
func TestCreateSubmitLandsInOneStep(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspace, "daily.go", "package payment\nconst Daily = 1\n")
	var created struct {
		ChangesetID string `json:"changeset_id"`
		WebURL      string `json:"web_url"`
		Submit      *struct {
			Status  string `json:"status"`
			Results []struct {
				ChangesetID string `json:"changeset_id"`
				Status      string `json:"status"`
				CommitID    string `json:"commit_id"`
			} `json:"results"`
		} `json:"submit"`
	}
	out := runCLI(t, home, workspace, "create", "--all", "--message", "daily sync", "--submit", "--json")
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("create --submit --json: %v\n%s", err, out)
	}
	// It reports the changeset landed, as the commit it landed as.
	if created.Submit == nil || created.Submit.Status != "submitted" || len(created.Submit.Results) != 1 ||
		created.Submit.Results[0].ChangesetID != created.ChangesetID || created.Submit.Results[0].Status != "submitted" ||
		!strings.HasPrefix(created.Submit.Results[0].CommitID, "sha256:") {
		t.Fatalf("create --submit --json = %s", out)
	}

	// A required check with no CI daemon to run it never reports.
	runCLI(t, home, workspace, "slice", "update", "acme/payment", "--required-check", "acme/payment/check")
	writeWorkspaceFile(t, workspace, "daily.go", "package payment\nconst Daily = 2\n")
	stdout, stderr := runCLIFails(t, home, workspace, "create", "--all", "--message", "gated sync", "--submit", "--watch-timeout", "2s")
	if !strings.HasPrefix(stdout, "created ") || !strings.Contains(stdout, "view: ") {
		t.Fatalf("the changeset should still be created:\n%s", stdout)
	}
	if !strings.Contains(stdout, `required check "acme/payment/check" has no result`) {
		t.Fatalf("stdout should say what blocks it:\n%s", stdout)
	}
	if !strings.Contains(stderr, `waiting for required check "acme/payment/check" to pass...`) ||
		!strings.Contains(stderr, "but could not submit it") || !strings.Contains(stderr, "gs submit") {
		t.Fatalf("stderr = %s", stderr)
	}
}
