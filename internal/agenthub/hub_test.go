package agenthub

import (
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestHubDaemonLifecycleAndEventFanout(t *testing.T) {
	h := New()
	c1 := h.RegisterDaemon("d1", "d1/inst-a", 2)
	c2 := h.RegisterDaemon("d1", "d1/inst-b", 4)
	if len(h.Conns("d1")) != 2 {
		t.Fatalf("Conns = %d, want 2", len(h.Conns("d1")))
	}
	if newest, ok := h.Daemon("d1"); !ok || newest != c2 {
		t.Fatalf("Daemon(d1) = %v, %v, want c2", newest, ok)
	}

	// Reconnecting same instance closes the old conn's Done channel.
	c1Replacement := h.RegisterDaemon("d1", "d1/inst-a", 2)
	select {
	case <-c1.Done:
	default:
		t.Fatal("expected replaced connection Done channel to be closed")
	}
	if c1.TrySend(&corev1.ServerMessage{}) {
		t.Fatal("expected TrySend on closed connection to return false")
	}

	c1Replacement.TrackRun("run_1")
	if !c1Replacement.HasRuns() {
		t.Fatal("expected HasRuns true after TrackRun")
	}
	c1Replacement.UntrackRun("run_1")
	if c1Replacement.HasRuns() {
		t.Fatal("expected HasRuns false after UntrackRun")
	}

	h.UnregisterDaemon(c2)
	h.UnregisterDaemon(c1Replacement)
	if len(h.OnlineDaemonIDs()) != 0 {
		t.Fatalf("OnlineDaemonIDs = %v, want empty", h.OnlineDaemonIDs())
	}

	ch, cancel := h.Subscribe("conv_1")
	defer cancel()
	ev := &corev1.ConversationEvent{Id: "ev_1"}
	h.Publish("conv_1", ev)
	if got := <-ch; got != ev {
		t.Fatalf("got event %v, want %v", got, ev)
	}
}
