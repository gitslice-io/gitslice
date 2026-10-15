package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSubmitStatusFollowsChecks: a submit blocked by a required check records
// why; the reason goes once the check reports (it described the last submit
// attempt), and gs submit waits for a check that reports while it waits.
func TestSubmitStatusFollowsChecks(t *testing.T) {
	ts := startTestServer(t)
	home, workspace := t.TempDir(), t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	runCLI(t, home, workspace, "slice", "update", "acme/payment", "--required-check", "acme/payment/check")

	type statusOutput struct {
		ChangesetID string `json:"changeset_id"`
		Status      string `json:"status"`
		Reason      string `json:"submit_blocked_reason"`
	}
	status := func(dir string) statusOutput {
		t.Helper()
		var out statusOutput
		if err := json.Unmarshal([]byte(runCLI(t, home, dir, "cs", "status", "--json")), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	writeWorkspaceFile(t, workspace, "gated.go", "package payment\nconst Gated = 1\n")
	runCLIFails(t, home, workspace, "create", "--all", "--message", "gated", "--submit", "--watch-timeout", "1s")
	first := status(workspace)
	if !strings.Contains(first.Reason, `required check "acme/payment/check" has no result`) {
		t.Fatalf("blocked reason = %q", first.Reason)
	}
	// A result for the patchset makes that reason stale.
	runCLI(t, home, workspace, "cs", "check", first.ChangesetID, "acme/payment/check", "--status", "fail")
	if got := status(workspace).Reason; got != "" {
		t.Fatalf("after the check reported, the reason is still %q", got)
	}
	// The next submit records the new reason; the check passing clears it.
	if _, err := runCLIResult(home, workspace, "submit", first.ChangesetID, "--watch-timeout", "2s"); err == nil || !strings.Contains(err.Error(), "is failing") {
		t.Fatalf("submit with a failing check: %v", err)
	}
	if got := status(workspace).Reason; !strings.Contains(got, "is failing") {
		t.Fatalf("blocked reason = %q, want the failing check", got)
	}
	runCLI(t, home, workspace, "cs", "check", first.ChangesetID, "acme/payment/check", "--status", "pass")
	if got := status(workspace).Reason; got != "" {
		t.Fatalf("after the check passed, the reason is still %q", got)
	}
	runCLI(t, home, workspace, "submit", first.ChangesetID)
	if got := status(workspace).Status; got != "submitted" {
		t.Fatalf("status = %q, want submitted", got)
	}

	// gs submit waits for a required check that has not reported yet.
	second := t.TempDir()
	runCLI(t, home, second, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, second, "later.go", "package payment\nconst Later = 1\n")
	runCLI(t, home, second, "create", "--all", "--message", "later")
	pending := status(second)
	reported := make(chan error, 1)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		_, err := runCLIResult(home, second, "cs", "check", pending.ChangesetID, "acme/payment/check", "--status", "pass")
		reported <- err
	}()
	stdout, stderr := runCLIStreams(t, home, second, "submit", pending.ChangesetID, "--watch-timeout", "60s")
	if err := <-reported; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, `waiting for required check "acme/payment/check" to pass...`) || !strings.Contains(stdout, "submitted") {
		t.Fatalf("submit:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}
