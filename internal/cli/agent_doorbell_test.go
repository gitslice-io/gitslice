package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/net/websocket"
)

// fakeDoorbell is a doorbell Worker: it answers pings, records the token,
// and says "wake" when rung.
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

// A daemon on the doorbell polls when it rings, not on the half-minute.
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
	go func() {
		for bell.opened.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		bell.ring <- struct{}{}
	}()
	start := time.Now()
	if err := d.standby(context.Background()); err != nil {
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

// When the socket drops, the daemon polls at once instead of waiting out
// the safety interval.
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
	go func() {
		// Wait for the socket, then let a poll see it open and choose the
		// long wait, then drop it.
		for bell.opened.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		bell.ring <- struct{}{} // poll 2 sees it connected
		for bell.opened.Load() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		bell.drop <- struct{}{}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.standby(ctx); err != nil {
		t.Fatalf("standby = %v; a drop should have made it poll", err)
	}
}

func TestOriginForDoorbell(t *testing.T) {
	if got := originFor("wss://doorbell.gitslice.io/wait"); got != "https://doorbell.gitslice.io" {
		t.Fatalf("origin = %s", got)
	}
}
