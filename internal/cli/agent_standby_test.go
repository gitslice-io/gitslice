package cli

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func standbyDaemon(poll func(context.Context) (*corev1.StandbyPollResponse, error)) (*agentDaemon, *bytes.Buffer) {
	var stderr bytes.Buffer
	return &agentDaemon{
		runner:        Runner{Stderr: &stderr},
		checkRuns:     map[string]context.CancelFunc{},
		detachAfter:   20 * time.Millisecond,
		standbyPollFn: poll,
	}, &stderr
}

// A detached daemon polls until the server says checks are queued, then
// returns so the daemon connects.
func TestStandbyPollsUntilChecksAreQueued(t *testing.T) {
	t.Setenv("GITSLICE_AGENT_POLL_INTERVAL", "10ms")
	var polls atomic.Int32
	d, stderr := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		n := polls.Add(1)
		return &corev1.StandbyPollResponse{DaemonId: "agent_1", Pending: n >= 4, PollAfterMs: 30000}, nil
	})
	start := time.Now()
	if err := d.standby(context.Background()); err != nil {
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
	if err := d.standby(ctx); err == nil {
		t.Fatal("standby returned nil after its context ended")
	}
}

// A server that predates StandbyPoll (or refuses it) never gets a detached
// daemon: it stays connected, as before.
func TestDaemonStaysConnectedToAServerWithoutStandby(t *testing.T) {
	for _, code := range []codes.Code{codes.Unimplemented, codes.FailedPrecondition} {
		d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
			return nil, grpcstatus.Error(code, "no")
		})
		if got := d.tryDetach(context.Background()); got != detachUnsupported {
			t.Fatalf("%s: tryDetach = %v, want unsupported", code, got)
		}
		if !d.standbyUnsupported.Load() || d.detaching.Load() {
			t.Fatalf("%s: the daemon must remember the server cannot, and not start detaching", code)
		}
	}
}

// With checks running, the daemon is not idle and never detaches; and when
// work is already queued for it, it stays connected to receive it.
func TestDaemonDoesNotDetachWhileBusyOrWhenWorkIsQueued(t *testing.T) {
	var polls atomic.Int32
	d, _ := standbyDaemon(func(context.Context) (*corev1.StandbyPollResponse, error) {
		polls.Add(1)
		return &corev1.StandbyPollResponse{Pending: true}, nil
	})
	d.checkRuns["run_1"] = func() {}
	if d.checksIdle() {
		t.Fatal("a daemon with a running check is idle")
	}
	delete(d.checkRuns, "run_1")
	d.checkRunOutboxes = map[string]*checkRunOutbox{"run_1": {}}
	if d.checksIdle() {
		t.Fatal("a daemon with unacknowledged check updates is idle")
	}
	delete(d.checkRunOutboxes, "run_1")
	if !d.checksIdle() {
		t.Fatal("a daemon with nothing to do is not idle")
	}
	if got := d.tryDetach(context.Background()); got != detachLater || d.detaching.Load() {
		t.Fatalf("tryDetach with work queued = %v, want to wait", got)
	}
}

func TestDetachAfterFlagAndEnv(t *testing.T) {
	cases := []struct {
		name    string
		in      agentStartOptions
		env     string
		want    time.Duration
		wantErr bool
	}{
		{"checks-only default", agentStartOptions{CI: true}, "", defaultDetachAfter, false},
		{"agent daemons never detach", agentStartOptions{}, "5m", 0, false},
		{"env", agentStartOptions{CI: true}, "45s", 45 * time.Second, false},
		{"env 0 stays connected", agentStartOptions{CI: true}, "0", 0, false},
		{"flag beats env", agentStartOptions{CI: true, DetachSet: true, DetachAfter: time.Minute}, "5m", time.Minute, false},
		{"flag 0 stays connected", agentStartOptions{CI: true, DetachSet: true}, "", 0, false},
		{"flag on an agent daemon", agentStartOptions{DetachSet: true, DetachAfter: time.Minute}, "", 0, true},
		{"negative", agentStartOptions{CI: true, DetachSet: true, DetachAfter: -time.Second}, "", 0, true},
		{"bad env", agentStartOptions{CI: true}, "soon", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GITSLICE_AGENT_DETACH_AFTER", c.env) // "" is the same as unset
			got, err := detachAfterFor(c.in)
			if (err != nil) != c.wantErr || (err == nil && got != c.want) {
				t.Fatalf("detachAfterFor = %v, %v; want %v (error %v)", got, err, c.want, c.wantErr)
			}
		})
	}
}
