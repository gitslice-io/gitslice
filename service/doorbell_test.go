package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/doorbell"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// A detached daemon is told where the doorbell is, counts as online while it
// waits there, and the server rings when a check is queued for it.
func TestStandbyPollUsesTheDoorbellAndQueuedChecksRingIt(t *testing.T) {
	rung := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			DaemonID string `json:"daemon_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		rung <- msg.DaemonID
		_, _ = w.Write([]byte(`{"listening":1}`))
	}))
	defer srv.Close()

	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	bell, err := doorbell.New(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	handlers.setDoorbell(bell)

	first, err := handlers.Agent.StandbyPoll(ctx, &corev1.StandbyPollRequest{Name: "ci", Runtime: ciDaemonRuntime})
	if err != nil || first.DoorbellUrl != "ws"+strings.TrimPrefix(srv.URL, "http")+"/wait" || !strings.HasPrefix(first.DoorbellToken, "gsdb1."+first.DaemonId+".") || first.SafetyPollAfterMs != doorbellSafetyPoll.Milliseconds() {
		t.Fatalf("poll = %v, %v", first, err)
	}
	if _, err := handlers.Agent.StandbyPoll(ctx, &corev1.StandbyPollRequest{Name: "ci", Runtime: ciDaemonRuntime, DoorbellOpen: true}); err != nil {
		t.Fatal(err)
	}
	if err := mem.Agents.SetDaemonStatus(ctx, first.DaemonId, "online"); err != nil {
		t.Fatal(err)
	}

	putChecksForPoolTest(t, mem, "c1")
	ref, _ := poolSlice(t, ctx, mem, "bell", []string{first.DaemonId}, "c1")
	base, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	createDispatchTestPatchset(t, ctx, handlers, ref, base.CommitId, "/acme/payment/bell/a.go", "package bell\n", "bell")
	select {
	case daemonID := <-rung:
		if daemonID != first.DaemonId {
			t.Fatalf("rang for %s, want %s", daemonID, first.DaemonId)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the doorbell was not rung")
	}
}
