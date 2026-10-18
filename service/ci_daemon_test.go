package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
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
