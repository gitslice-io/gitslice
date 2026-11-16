package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/cli"
)

// TestCheckCommandsAndFailingSubmitTail covers fb_d155: a submit blocked by a
// failing required check prints the end of its log, gs check list|logs|rerun
// work by check name, and gs rpc call streams a server-streaming RPC.
func TestCheckCommandsAndFailingSubmitTail(t *testing.T) {
	t.Setenv("GITSLICE_AGENT_POLL_INTERVAL", "200ms")
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/payment/.gitslice/checks.yaml",
		"version: 1\nchecks:\n  unit:\n    run: \"echo first-line; echo check-log-marker; exit 1\"\n")
	runCLI(t, home, workspace, "create", "--message", "checks", "--all", "--submit")

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		r := cli.Runner{Home: home, Dir: t.TempDir(), Stdout: &lockedBuffer{}, Stderr: &lockedBuffer{}}
		done <- r.Run(ctx, []string{"agent", "start", "--ci", "--name", "check-cmd-ci"})
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the daemon did not stop")
		}
	}()
	var listed struct {
		Daemons []struct {
			ID string `json:"id"`
		} `json:"daemons"`
	}
	waitUntil(t, "the daemon to come online", 20*time.Second, func() bool {
		return json.Unmarshal([]byte(runCLI(t, home, workspace, "agent", "status", "--json")), &listed) == nil && len(listed.Daemons) == 1
	})
	runCLI(t, home, workspace, "slice", "set-ci-daemon", "acme/payment", listed.Daemons[0].ID)
	runCLI(t, home, workspace, "slice", "update", "acme/payment", "--required-check", "payment/unit")

	writeWorkspaceFile(t, workspace, "acme/payment/a.go", "package payment\n")
	_, stderr := runCLIFails(t, home, workspace, "create", "--all", "--message", "failing", "--submit", "--watch-timeout", "60s")
	if !strings.Contains(stderr, "last lines of payment/unit") || !strings.Contains(stderr, "check-log-marker") {
		t.Fatalf("a blocked submit should show the failing check's log:\n%s", stderr)
	}

	list := runCLI(t, home, workspace, "check", "list")
	if !strings.Contains(list, "failed") || !strings.Contains(list, "payment/unit") || !strings.Contains(list, "run_") {
		t.Fatalf("gs check list =\n%s", list)
	}
	if logs := runCLI(t, home, workspace, "check", "logs", "unit"); !strings.Contains(logs, "first-line") || !strings.Contains(logs, "check-log-marker") {
		t.Fatalf("gs check logs unit =\n%s", logs)
	}
	if tail := runCLI(t, home, workspace, "check", "logs", "payment/unit", "--tail", "1"); strings.Contains(tail, "first-line") {
		t.Fatalf("gs check logs --tail 1 =\n%s", tail)
	}
	var checks struct {
		Checks []struct {
			RunID string `json:"run_id"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "check", "list", "--json")), &checks); err != nil || len(checks.Checks) != 1 {
		t.Fatalf("gs check list --json: %+v %v", checks, err)
	}
	stream := runCLI(t, home, workspace, "rpc", "call", "gitslice.core.v1.CheckService/StreamCheckRun", `{"run_id":"`+checks.Checks[0].RunID+`"}`)
	lines := strings.Split(strings.TrimSpace(stream), "\n")
	if len(lines) == 0 || !strings.Contains(stream, "check-log-marker") || !strings.HasPrefix(lines[0], "{") {
		t.Fatalf("gs rpc call StreamCheckRun =\n%s", stream)
	}
	if rerun := runCLI(t, home, workspace, "check", "rerun", "unit"); !strings.Contains(rerun, "rerunning payment/unit") {
		t.Fatalf("gs check rerun =\n%s", rerun)
	}
}
