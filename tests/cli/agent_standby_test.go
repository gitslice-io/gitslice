package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/cli"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// lockedBuffer is an io.Writer a test can read while a daemon writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitUntil(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}

// TestCIDaemonDetachesWhenIdleAndWakesForQueuedChecks runs a real checks-only
// daemon against a real server. With nothing to run it closes its stream, so
// the server has nothing to hold open, yet it stays online; a check queued
// meanwhile (a new patchset, then a rerun) is picked up on its next poll.
func TestCIDaemonDetachesWhenIdleAndWakesForQueuedChecks(t *testing.T) {
	t.Setenv("GITSLICE_AGENT_POLL_INTERVAL", "200ms")
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/payment/.gitslice/checks.yaml", "version: 1\nchecks:\n  unit:\n    run: \"true\"\n")
	runCLI(t, home, workspace, "create", "--message", "checks", "--all", "--submit")

	daemonDir := t.TempDir()
	stderr := &lockedBuffer{}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		r := cli.Runner{Home: home, Dir: daemonDir, Stdout: &lockedBuffer{}, Stderr: stderr}
		done <- r.Run(ctx, []string{"agent", "start", "--ci", "--name", "detach-ci", "--detach-after", "1s"})
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the daemon did not stop")
		}
	}()

	detaches := func() int { return strings.Count(stderr.String(), "detached from the server") }
	waitUntil(t, "the idle daemon to detach", 20*time.Second, func() bool { return detaches() >= 1 })

	var listed struct {
		Daemons []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"daemons"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "agent", "status", "--json")), &listed); err != nil || len(listed.Daemons) != 1 {
		t.Fatalf("gs agent status = %+v, %v", listed, err)
	}
	if d := listed.Daemons[0]; d.Name != "detach-ci" || d.Status != "online" {
		t.Fatalf("a detached daemon must still be online: %+v", d)
	}
	runCLI(t, home, workspace, "slice", "set-ci-daemon", "acme/payment", listed.Daemons[0].ID)

	// A patchset made while the daemon is detached queues a check; the
	// daemon's next poll finds it, reconnects, and runs it.
	writeWorkspaceFile(t, workspace, "acme/payment/a.go", "package payment\n")
	runCLI(t, home, workspace, "create", "--message", "change", "--all")
	state := workspaceState(t, workspace)

	grpcCtx, cancel := context.WithTimeout(grpcAuthContext(readToken(t, home)), 60*time.Second)
	defer cancel()
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	checks := corev1.NewCheckServiceClient(conn)
	var firstRun string
	defer func() {
		if t.Failed() {
			res, err := checks.ListCheckRuns(grpcCtx, &corev1.ListCheckRunsRequest{ChangesetId: state.CurrentChangesetID})
			t.Logf("check runs: %v (err %v)\ndaemon stderr:\n%s", res.GetRuns(), err, stderr.String())
		}
	}()
	waitUntil(t, "the detached daemon to run the queued check", 30*time.Second, func() bool {
		res, err := checks.ListCheckRuns(grpcCtx, &corev1.ListCheckRunsRequest{ChangesetId: state.CurrentChangesetID, PatchsetId: state.CurrentPatchsetID})
		if err != nil {
			return false
		}
		for _, run := range res.Runs {
			if run.CheckName == "payment/unit" && run.Status == "passed" {
				firstRun = run.Id
				return true
			}
		}
		return false
	})
	if !strings.Contains(stderr.String(), "connecting") {
		t.Fatalf("the daemon did not report waking up:\n%s", stderr.String())
	}

	// Once it is idle and detached again, a rerun still works: the server
	// queues it for the daemon instead of saying the daemon is not online.
	waitUntil(t, "the daemon to detach again", 20*time.Second, func() bool { return detaches() >= 2 })
	rerun, err := checks.RerunCheck(grpcCtx, &corev1.RerunCheckRequest{RunId: firstRun})
	if err != nil {
		t.Fatalf("RerunCheck for a detached daemon: %v", err)
	}
	waitUntil(t, "the rerun to pass", 30*time.Second, func() bool {
		run, err := checks.GetCheckRun(grpcCtx, &corev1.GetCheckRunRequest{RunId: rerun.Id})
		return err == nil && run.Status == "passed"
	})
}
