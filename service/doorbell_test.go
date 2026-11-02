package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// The same vector as doorbell/test/auth.test.ts.
func TestDoorbellTokenMatchesTheWorker(t *testing.T) {
	bell, err := NewDoorbell("https://doorbell.example.com", "s")
	if err != nil {
		t.Fatal(err)
	}
	want := "gsdb1.agent_1.2000000000.54f58130863f0a6be9c2a336d3eb4c558e62cba8072aaa80e8c3b8d7f6429e31"
	if got := bell.Token("agent_1", time.Unix(2000000000, 0)); got != want {
		t.Fatalf("token = %s, want %s", got, want)
	}
	if got := bell.WaitURL(); got != "wss://doorbell.example.com/wait" {
		t.Fatalf("wait URL = %s", got)
	}
}

func TestDoorbellRingSignsAndReadsListeners(t *testing.T) {
	var got struct {
		DaemonID string `json:"daemon_id"`
		TS       int64  `json:"ts"`
	}
	var signature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		signature = r.Header.Get("X-Gitslice-Doorbell-Signature")
		bell, _ := NewDoorbell("http://x", "secret")
		if r.URL.Path != "/ring" || signature != bell.mac("ring", string(body)) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"listening":2}`))
	}))
	defer srv.Close()
	bell, err := NewDoorbell(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	listening, err := bell.Ring(context.Background(), "agent_1", 1)
	if err != nil || listening != 2 || got.DaemonID != "agent_1" || got.TS == 0 {
		t.Fatalf("ring = %d, %v; server saw %+v", listening, err, got)
	}
	wrong, _ := NewDoorbell(srv.URL, "wrong")
	if _, err := wrong.Ring(context.Background(), "agent_1", 1); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("ring with the wrong secret = %v, want 401", err)
	}
	if none, err := NewDoorbell("", ""); none != nil || err != nil {
		t.Fatalf("no doorbell = %v, %v", none, err)
	}
}

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
	bell, err := NewDoorbell(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	handlers.SetDoorbell(bell)

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
