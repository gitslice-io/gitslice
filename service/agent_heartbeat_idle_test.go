package service

import (
	"context"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestDaemonConnectPathIsAgentConnect(t *testing.T) {
	if activity.DaemonConnectPath != corev1.AgentService_Connect_FullMethodName {
		t.Fatalf("activity.DaemonConnectPath = %q, want %q", activity.DaemonConnectPath, corev1.AgentService_Connect_FullMethodName)
	}
}

// An idle server takes a connected daemon's heartbeats without touching the
// database, except once every idleDaemonStatusEvery, and as usual while the
// daemon's process runs checks or the server is busy.
func TestDaemonHeartbeatLeavesTheDatabaseAloneWhileIdle(t *testing.T) {
	prev := activity.Default
	activity.Default = activity.NewIdle(time.Minute)
	t.Cleanup(func() { activity.Default = prev })

	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	daemon, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: "user_alice", Account: "acme", Name: "idle-daemon", Runtime: "ci",
	})
	if err != nil {
		t.Fatalf("RegisterDaemon: %v", err)
	}
	conn := &daemonConn{daemonID: daemon.Id, instance: "idle-daemon/1"}
	svc := handlers.Agent

	// wrote reports whether the heartbeat wrote the daemon's status.
	heartbeat := func(at, now time.Time) (time.Time, bool) {
		t.Helper()
		if err := mem.Agents.SetDaemonStatus(ctx, daemon.Id, "offline"); err != nil {
			t.Fatalf("SetDaemonStatus: %v", err)
		}
		next := svc.daemonHeartbeat(ctx, daemon.Id, conn, at, now)
		got, err := mem.Agents.GetDaemon(ctx, daemon.Id)
		if err != nil {
			t.Fatalf("GetDaemon: %v", err)
		}
		return next, got.GetStatus() == "online"
	}

	start := time.Now()
	at, wrote := heartbeat(time.Time{}, start)
	if !wrote || !at.Equal(start) {
		t.Fatalf("first idle heartbeat: wrote %v at %v, want a write at %v", wrote, at, start)
	}
	if at, wrote = heartbeat(at, start.Add(10*time.Minute)); wrote || !at.Equal(start) {
		t.Fatalf("idle heartbeat 10m later: wrote %v at %v, want no write", wrote, at)
	}
	later := start.Add(idleDaemonStatusEvery)
	if at, wrote = heartbeat(at, later); !wrote || !at.Equal(later) {
		t.Fatalf("idle heartbeat after %s: wrote %v at %v, want a write", idleDaemonStatusEvery, wrote, at)
	}

	conn.trackRun("run_1")
	if at, wrote = heartbeat(later, later.Add(time.Minute)); !wrote || !at.IsZero() {
		t.Fatalf("heartbeat while running a check: wrote %v at %v, want a write", wrote, at)
	}
	conn.untrackRun("run_1")

	activity.Default.Begin()()
	if at, wrote = heartbeat(later, later.Add(time.Minute)); !wrote || !at.IsZero() {
		t.Fatalf("heartbeat while the server is busy: wrote %v at %v, want a write", wrote, at)
	}
}
