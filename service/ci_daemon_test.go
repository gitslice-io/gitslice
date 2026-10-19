package service

import (
	"context"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// connectTestDaemon registers a daemon over a fake stream and returns its id
// and a func that disconnects it.
func connectTestDaemon(t *testing.T, ctx context.Context, handlers *Handlers, name, runtime string) (string, func()) {
	t.Helper()
	stream := newFakeAgentConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:    name,
		Runtime: runtime,
	}}}
	registered := (<-stream.sent).GetRegistered()
	if registered == nil || registered.DaemonId == "" {
		t.Fatalf("registered message = %#v, want daemon id", registered)
	}
	return registered.DaemonId, func() {
		close(stream.recv)
		if err := <-errCh; err != nil {
			t.Fatalf("Connect returned error: %v", err)
		}
	}
}

func TestCreateConversationRefusesACIOnlyDaemon(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	daemonID, disconnect := connectTestDaemon(t, ctx, handlers, "my-ci", ciDaemonRuntime)
	defer disconnect()

	_, err := handlers.Agent.CreateConversation(ctx, &corev1.CreateConversationRequest{
		DaemonId: daemonID,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:    "should not start",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("CreateConversation error = %v, want FailedPrecondition", err)
	}
}

func TestCheckRunsSayWhetherTheirDaemonIsOnline(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	daemonID, disconnect := connectTestDaemon(t, ctx, handlers, "my-ci", ciDaemonRuntime)

	run := &corev1.CheckRun{Id: "run_1", DaemonId: daemonID, Status: "queued"}
	handlers.Check.annotateDaemons(ctx, run)
	if !run.DaemonOnline || run.DaemonLastSeenAt == "" {
		t.Fatalf("run = %#v, want its daemon online with a last-seen time", run)
	}

	disconnect()
	run = &corev1.CheckRun{Id: "run_1", DaemonId: daemonID, Status: "queued"}
	handlers.Check.annotateDaemons(ctx, run)
	if run.DaemonOnline || run.DaemonLastSeenAt == "" {
		t.Fatalf("run = %#v, want its daemon offline with a last-seen time", run)
	}

	// A run without a daemon stays as it is.
	bundled := &corev1.CheckRun{Id: "run_2", Provenance: "self"}
	handlers.Check.annotateDaemons(ctx, bundled)
	if bundled.DaemonOnline || bundled.DaemonLastSeenAt != "" {
		t.Fatalf("bundled run = %#v, want no daemon status", bundled)
	}
}

// A checks-only daemon that detaches on purpose stays online: it polls with
// StandbyPoll, and holds no stream. Any other end of the stream marks it
// offline.
func TestDetachedCIDaemonStaysOnline(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	stream := newFakeAgentConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{Name: "my-ci", Runtime: ciDaemonRuntime}}}
	daemonID := (<-stream.sent).GetRegistered().GetDaemonId()
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Detach{Detach: &corev1.Detach{}}}
	if err := <-errCh; err != nil {
		t.Fatalf("Connect after a detach returned %v, want a clean end", err)
	}
	if _, connected := handlers.Agent.hub.daemon(daemonID); connected {
		t.Fatal("a detached daemon is still in the hub")
	}
	daemon, err := handlers.Agent.Agents.GetDaemon(ctx, daemonID)
	if err != nil || daemon.Status != "online" {
		t.Fatalf("detached daemon = %v, %v; want online", daemon, err)
	}

	// A stream that ends any other way marks the daemon offline.
	other, disconnect := connectTestDaemon(t, ctx, handlers, "other-ci", ciDaemonRuntime)
	disconnect()
	if d, _ := handlers.Agent.Agents.GetDaemon(ctx, other); d.GetStatus() != "offline" {
		t.Fatalf("daemon after a dropped stream = %v, want offline", d.GetStatus())
	}
}

// Only a checks-only daemon may detach: an agent daemon needs its stream for
// conversations.
func TestAgentDaemonCannotDetach(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	stream := newFakeAgentConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- handlers.Agent.Connect(stream) }()
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{Name: "bot", Runtime: "codex"}}}
	<-stream.sent
	stream.recv <- &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Detach{Detach: &corev1.Detach{}}}
	if err := <-errCh; status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Connect error = %v, want FailedPrecondition", err)
	}
}

func TestStandbyPollRegistersTheDaemonAndSaysWhenChecksAreQueued(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	if _, err := handlers.Agent.StandbyPoll(ctx, &corev1.StandbyPollRequest{Name: "bot", Runtime: "codex"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("an agent daemon's poll = %v, want FailedPrecondition", err)
	}

	first, err := handlers.Agent.StandbyPoll(ctx, &corev1.StandbyPollRequest{Name: "my-ci", Runtime: ciDaemonRuntime, Version: "v1"})
	if err != nil || first.DaemonId == "" || first.Pending || first.PollAfterMs != standbyPollAfter.Milliseconds() {
		t.Fatalf("first poll = %v, %v; want a registered daemon with nothing pending", first, err)
	}
	daemon, err := handlers.Agent.Agents.GetDaemon(ctx, first.DaemonId)
	if err != nil || daemon.Status != "online" || daemon.Runtime != ciDaemonRuntime {
		t.Fatalf("polled daemon = %v, %v", daemon, err)
	}

	handlers.Agent.Checks = &sweepFakeCheckStore{runs: []*corev1.CheckRun{{Id: "run-1", DaemonId: first.DaemonId, Status: "queued"}}}
	second, err := handlers.Agent.StandbyPoll(ctx, &corev1.StandbyPollRequest{Name: "my-ci", Runtime: ciDaemonRuntime})
	if err != nil || second.DaemonId != first.DaemonId || !second.Pending {
		t.Fatalf("poll with a queued run = %v, %v; want the same daemon, pending", second, err)
	}
}

// A daemon nobody has heard from for a while is offline, whatever the store
// last saw it do: an instance that stopped without closing its streams used
// to leave daemons online for ever.
func TestEffectiveDaemonStatusNeedsARecentSignOfLife(t *testing.T) {
	now := time.Now()
	cases := []struct {
		status   string
		lastSeen time.Time
		want     string
	}{
		{"online", now.Add(-10 * time.Second), "online"},
		{"online", now.Add(-storage.DaemonStaleAfter - time.Second), "offline"},
		{"offline", now, "offline"},
	}
	for _, c := range cases {
		if got := storage.EffectiveDaemonStatus(c.status, c.lastSeen, now); got != c.want {
			t.Errorf("EffectiveDaemonStatus(%s, %s ago) = %s, want %s", c.status, now.Sub(c.lastSeen), got, c.want)
		}
	}
}
