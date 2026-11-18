package agentd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/net/websocket"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func standbyDaemon(poll func(context.Context) (*corev1.StandbyPollResponse, error)) (*Daemon, *SafeBuffer) {
	var stderr SafeBuffer
	return &Daemon{
		Host:          HostCallbacks{Stderr: &stderr},
		checkRuns:     map[string]context.CancelFunc{},
		DetachAfter:   20 * time.Millisecond,
		StandbyPollFn: poll,
	}, &stderr
}

func TestStandbyPollsUntilChecksAreQueued(t *testing.T) {
	t.Setenv("GITSLICE_AGENT_POLL_INTERVAL", "10ms")
	var polls atomic.Int32
	d, stderr := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		n := polls.Add(1)
		return &corev1.StandbyPollResponse{DaemonId: "agent_1", Pending: n >= 4, PollAfterMs: 30000}, nil
	})
	start := time.Now()
	if err := d.Standby(context.Background()); err != nil {
		t.Fatalf("standby = %v, want nil once checks are queued", err)
	}
	if polls.Load() != 4 {
		t.Fatalf("polled %d times, want 4", polls.Load())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s; the override should beat the server's 30s hint", took)
	}
	if got := stderr.String(); got == "" {
		t.Fatal("standby said nothing about detaching")
	}
}

func TestStandbyStopsWhenTheDaemonStops(t *testing.T) {
	d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		return &corev1.StandbyPollResponse{PollAfterMs: 60000}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := d.Standby(ctx); err == nil {
		t.Fatal("standby returned nil after its context ended")
	}
}

func TestDaemonStaysConnectedToAServerWithoutStandby(t *testing.T) {
	for _, code := range []codes.Code{codes.Unimplemented, codes.FailedPrecondition} {
		d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
			return nil, grpcstatus.Error(code, "no")
		})
		if got := d.TryDetach(context.Background()); got != DetachUnsupported {
			t.Fatalf("%s: tryDetach = %v, want unsupported", code, got)
		}
		if !d.StandbyUnsupported.Load() || d.Detaching.Load() {
			t.Fatalf("%s: the daemon must remember the server cannot, and not start detaching", code)
		}
	}
}

func TestDaemonDoesNotDetachWhileBusyOrWhenWorkIsQueued(t *testing.T) {
	var polls atomic.Int32
	d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		polls.Add(1)
		return &corev1.StandbyPollResponse{Pending: true}, nil
	})
	d.checkRuns["run_1"] = func() {}
	if d.ChecksIdle() {
		t.Fatal("a daemon with a running check is idle")
	}
	delete(d.checkRuns, "run_1")
	d.checkRunOutboxes = map[string]*CheckRunOutbox{"run_1": {}}
	if d.ChecksIdle() {
		t.Fatal("a daemon with unacknowledged check updates is idle")
	}
	delete(d.checkRunOutboxes, "run_1")
	if !d.ChecksIdle() {
		t.Fatal("a daemon with nothing to do is not idle")
	}
	if got := d.TryDetach(context.Background()); got != DetachLater || d.Detaching.Load() {
		t.Fatalf("tryDetach with work queued = %v, want to wait", got)
	}
}

func TestDetachAfterFlagAndEnv(t *testing.T) {
	cases := []struct {
		name      string
		ci        bool
		detachSet bool
		detach    time.Duration
		env       string
		want      time.Duration
		wantErr   bool
	}{
		{"checks-only default", true, false, 0, "", DefaultDetachAfter, false},
		{"agent daemons never detach", false, false, 0, "5m", 0, false},
		{"env", true, false, 0, "45s", 45 * time.Second, false},
		{"env 0 stays connected", true, false, 0, "0", 0, false},
		{"flag beats env", true, true, time.Minute, "5m", time.Minute, false},
		{"flag 0 stays connected", true, true, 0, "", 0, false},
		{"flag on an agent daemon", false, true, time.Minute, "", 0, true},
		{"negative", true, true, -time.Second, "", 0, true},
		{"bad env", true, false, 0, "soon", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GITSLICE_AGENT_DETACH_AFTER", c.env)
			got, err := DetachAfterFor(c.ci, c.detachSet, c.detach)
			if (err != nil) != c.wantErr || (err == nil && got != c.want) {
				t.Fatalf("DetachAfterFor = %v, %v; want %v (error %v)", got, err, c.want, c.wantErr)
			}
		})
	}
}

func TestStandbyRetriesAFailedPollInsteadOfConnecting(t *testing.T) {
	t.Setenv("GITSLICE_AGENT_POLL_INTERVAL", "10ms")
	var polls atomic.Int32
	d, stderr := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		switch n := polls.Add(1); {
		case n <= 3:
			return nil, grpcstatus.Error(codes.DeadlineExceeded, "context deadline exceeded")
		case n == 4:
			return &corev1.StandbyPollResponse{PollAfterMs: 30000}, nil
		default:
			return &corev1.StandbyPollResponse{Pending: true}, nil
		}
	})
	if err := d.Standby(context.Background()); err != nil {
		t.Fatalf("standby = %v, want nil once checks are queued", err)
	}
	if polls.Load() != 5 {
		t.Fatalf("polled %d times, want 5: three failures and an empty poll are not a reason to connect", polls.Load())
	}
	out := stderr.String()
	if !bytes.Contains([]byte(out), []byte("standby poll failed")) || !bytes.Contains([]byte(out), []byte("trying again")) {
		t.Fatalf("standby did not say it would retry:\n%s", out)
	}
	if bytes.Contains([]byte(out), []byte("reconnecting")) {
		t.Fatalf("a failed poll was treated as a reason to connect:\n%s", out)
	}
	if !bytes.Contains([]byte(out), []byte("a check is queued: connecting")) {
		t.Fatalf("standby did not say why it connected:\n%s", out)
	}
}

func TestStandbyRetryWaitBacksOffToTheCap(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := StandbyRetryWait(i + 1); got != w {
			t.Fatalf("StandbyRetryWait(%d) = %s, want %s", i+1, got, w)
		}
	}
	if StandbyRetryWait(0) != 30*time.Second || StandbyRetryWait(1000) != 5*time.Minute {
		t.Fatal("StandbyRetryWait is out of range at its ends")
	}
}

type fakeDoorbell struct {
	srv    *httptest.Server
	token  atomic.Value
	ring   chan struct{}
	drop   chan struct{}
	opened atomic.Int32
}

func newFakeDoorbell(t *testing.T) *fakeDoorbell {
	t.Helper()
	b := &fakeDoorbell{ring: make(chan struct{}, 1), drop: make(chan struct{}, 1)}
	ws := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(conn *websocket.Conn) {
			b.token.Store(conn.Request().Header.Get("Authorization"))
			b.opened.Add(1)
			go func() {
				for {
					var msg string
					if websocket.Message.Receive(conn, &msg) != nil {
						return
					}
					if msg == "ping" {
						_ = websocket.Message.Send(conn, "pong")
					}
				}
			}()
			select {
			case <-b.ring:
				_ = websocket.Message.Send(conn, "wake")
				time.Sleep(100 * time.Millisecond)
			case <-b.drop:
			}
			conn.Close()
		},
	}
	b.srv = httptest.NewServer(ws)
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeDoorbell) waitURL() string {
	return "ws" + strings.TrimPrefix(b.srv.URL, "http") + "/wait"
}

func TestStandbyPollsWhenTheDoorbellRings(t *testing.T) {
	t.Setenv("GITSLICE_AGENT_POLL_INTERVAL", "")
	bell := newFakeDoorbell(t)
	var polls atomic.Int32
	d, stderr := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		n := polls.Add(1)
		return &corev1.StandbyPollResponse{
			DaemonId:          "agent_1",
			Pending:           n >= 2,
			PollAfterMs:       60_000,
			DoorbellUrl:       bell.waitURL(),
			DoorbellToken:     "gsdb1.agent_1.9.x",
			SafetyPollAfterMs: 20 * 60_000,
		}, nil
	})
	d.Host.ServerAddr = "127.0.0.1:50051"
	go func() {
		for bell.opened.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		bell.ring <- struct{}{}
	}()
	start := time.Now()
	if err := d.Standby(context.Background()); err != nil {
		t.Fatalf("standby = %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %s; the ring should have woken it", took)
	}
	if polls.Load() != 2 {
		t.Fatalf("polled %d times, want 2", polls.Load())
	}
	if got, _ := bell.token.Load().(string); got != "Bearer gsdb1.agent_1.9.x" {
		t.Fatalf("the doorbell saw %q", got)
	}
	if !strings.Contains(stderr.String(), "doorbell rang") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestStandbyPollsWhenTheDoorbellDrops(t *testing.T) {
	t.Setenv("GITSLICE_AGENT_POLL_INTERVAL", "")
	bell := newFakeDoorbell(t)
	var polls atomic.Int32
	d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		n := polls.Add(1)
		return &corev1.StandbyPollResponse{
			Pending:           n >= 3,
			PollAfterMs:       60_000,
			DoorbellUrl:       bell.waitURL(),
			DoorbellToken:     "t",
			SafetyPollAfterMs: 20 * 60_000,
		}, nil
	})
	d.Host.ServerAddr = "127.0.0.1:50051"
	go func() {
		for bell.opened.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		bell.ring <- struct{}{}
		for bell.opened.Load() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		bell.drop <- struct{}{}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Standby(ctx); err != nil {
		t.Fatalf("standby = %v; a drop should have made it poll", err)
	}
}

func TestStandbyWaitsForInitialDoorbellReadyBeforeChoosingPollInterval(t *testing.T) {
	wsDone := make(chan struct{})
	srv := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		<-wsDone
		_ = conn.Close()
	}))
	defer srv.Close()
	defer close(wsDone)

	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://")

	var polls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		n := polls.Add(1)
		if n >= 2 {
			return &corev1.StandbyPollResponse{Pending: true}, nil
		}
		return &corev1.StandbyPollResponse{
			DaemonId:          "agent_race_test",
			PollAfterMs:       80,
			SafetyPollAfterMs: 3600000,
			DoorbellUrl:       wsURL,
			DoorbellToken:     "tok_test",
		}, nil
	})
	d.Host.ServerAddr = "127.0.0.1:50051"

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Standby(ctx)
	}()

	time.Sleep(250 * time.Millisecond)
	if got := polls.Load(); got != 1 {
		cancel()
		t.Fatalf("standby polled %d times within 250ms, want 1 (initial doorbell race regressed)", got)
	}
	if !d.DoorbellOpen.Load() {
		cancel()
		t.Fatal("expected d.DoorbellOpen to be true on first cycle after waitReady")
	}
	cancel()
	<-errCh
}
