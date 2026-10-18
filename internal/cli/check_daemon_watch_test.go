package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestCheckDaemonWatchSaysOnceThatTheDaemonIsOffline(t *testing.T) {
	now := time.Date(2026, 10, 18, 19, 20, 0, 0, time.UTC)
	var out bytes.Buffer
	w := newCheckDaemonWatch(&out)
	offline := []*corev1.CheckRun{
		{CheckName: "test", Status: "queued", DaemonId: "agent_1", DaemonName: "grok-bot-ci", DaemonLastSeenAt: "2026-10-18T18:56:57.799529Z"},
		{CheckName: "vet", Status: "queued", DaemonId: "agent_1", DaemonName: "grok-bot-ci", DaemonLastSeenAt: "2026-10-18T18:56:57.799529Z"},
	}
	w.note(offline, "test", now)
	w.note(offline, "test", now)
	want := `CI daemon grok-bot-ci is offline (last seen 18:56 UTC, 23m ago); "test" starts when it reconnects (or when the slice names another CI daemon)` + "\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}

	// Back online, then gone again: said again.
	online := []*corev1.CheckRun{{CheckName: "test", Status: "queued", DaemonId: "agent_1", DaemonOnline: true, DaemonLastSeenAt: "2026-10-18T19:20:00Z"}}
	w.note(online, "test", now)
	w.note(offline, "test", now)
	if got := strings.Count(out.String(), "is offline"); got != 2 {
		t.Fatalf("said offline %d times, want 2: %q", got, out.String())
	}
}

func TestCheckDaemonWatchSaysNothingWithoutDaemonStatus(t *testing.T) {
	var out bytes.Buffer
	w := newCheckDaemonWatch(&out)
	// An older server fills neither daemon_online nor daemon_last_seen_at.
	w.note([]*corev1.CheckRun{{CheckName: "test", Status: "queued", DaemonId: "agent_1"}}, "test", time.Now())
	// Running or finished runs and bundled runs are not waiting on a daemon.
	w.note([]*corev1.CheckRun{
		{CheckName: "test", Status: "running", DaemonId: "agent_1", DaemonLastSeenAt: "2026-10-18T18:56:57Z"},
		{CheckName: "test", Status: "queued", DaemonLastSeenAt: "2026-10-18T18:56:57Z"},
	}, "test", time.Now())
	if out.Len() != 0 {
		t.Fatalf("output = %q, want nothing", out.String())
	}
}

func TestLastSeenPhrase(t *testing.T) {
	now := time.Date(2026, 10, 18, 19, 20, 0, 0, time.UTC)
	cases := map[string]string{
		"2026-10-18T19:19:30Z": " (last seen 19:19 UTC)",
		"2026-10-18T16:00:00Z": " (last seen 16:00 UTC, 3h ago)",
		"2026-10-17T16:00:00Z": " (last seen 2026-10-17 16:00 UTC, 27h ago)",
		"2026-10-10T16:00:00Z": " (last seen 2026-10-10 16:00 UTC, 8d ago)",
		"":                     "",
	}
	for in, want := range cases {
		if got := lastSeenPhrase(in, now); got != want {
			t.Errorf("lastSeenPhrase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaptureLeavesChecksItCannotRunToTheCIDaemon(t *testing.T) {
	bundled, left := bundleWorkspaceResults([]WorkspaceCheckResult{
		{Name: "gofmt", Status: "passed"},
		{Name: "e2e", Status: "errored", Summary: "no container runtime found (install docker or podman)", Unrunnable: true},
		{Name: "vet", Status: "failed", ExitCode: 1},
	})
	if len(bundled) != 2 || bundled[0].Name != "gofmt" || bundled[1].Name != "vet" {
		t.Fatalf("bundled = %v, want gofmt and vet", bundled)
	}
	if len(left) != 1 || left[0].Name != "e2e" {
		t.Fatalf("left to CI = %v, want e2e", left)
	}
}
