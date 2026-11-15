package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// The doorbell (design/32_ci_dispatch_on_cloudflare.md, layer 1): a
// detached checks-only daemon holds a WebSocket open to a Cloudflare Worker,
// which says "wake" when the server queues a check for it. The daemon then
// polls the server only on a wake and every safety interval, instead of
// every half minute. While the socket is down it polls as before.
const (
	doorbellPingEvery  = 30 * time.Second
	doorbellReadWithin = 75 * time.Second
	doorbellDialWithin = 15 * time.Second
	doorbellRetryMax   = time.Minute
)

type doorbellListener struct {
	mu        sync.Mutex
	url       string
	token     string
	connected bool
	// say reports the socket opening and failing, once per change.
	say      func(string)
	lastFail string

	started time.Time

	wake   chan struct{}
	lost   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

// listenDoorbell starts waiting on the doorbell at waitURL with token. say
// (may be nil) hears when the socket opens and why it fails.
func listenDoorbell(ctx context.Context, waitURL, token string, say func(string)) *doorbellListener {
	ctx, cancel := context.WithCancel(ctx)
	if say == nil {
		say = func(string) {}
	}
	l := &doorbellListener{
		say:     say,
		url:     waitURL,
		token:   token,
		wake:    make(chan struct{}, 1),
		lost:    make(chan struct{}, 1),
		cancel:  cancel,
		done:    make(chan struct{}),
		started: time.Now(),
	}
	go l.run(ctx)
	return l
}

// update gives the listener a fresh token (and URL) for its next dial.
func (l *doorbellListener) update(waitURL, token string) {
	l.mu.Lock()
	l.url, l.token = waitURL, token
	l.mu.Unlock()
}

func (l *doorbellListener) stop() {
	if l == nil {
		return
	}
	l.cancel()
	<-l.done
}

// isConnected reports whether the socket is open.
func (l *doorbellListener) isConnected() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connected
}

// woken and dropped are nil for a nil listener, so a select on them waits
// for ever.
func (l *doorbellListener) woken() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.wake
}

func (l *doorbellListener) dropped() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.lost
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (l *doorbellListener) setConnected(connected bool) {
	l.mu.Lock()
	was := l.connected
	l.connected = connected
	l.mu.Unlock()
	if was && !connected {
		notify(l.lost)
	}
}

func (l *doorbellListener) run(ctx context.Context) {
	defer close(l.done)
	retry := time.Second
	for ctx.Err() == nil {
		l.mu.Lock()
		waitURL, token := l.url, l.token
		l.mu.Unlock()
		opened := time.Now()
		err := l.listenOnce(ctx, waitURL, token)
		l.setConnected(false)
		if ctx.Err() != nil {
			return
		}
		if why := fmt.Sprint(err); why != l.lastFail {
			l.lastFail = why
			l.say(fmt.Sprintf("doorbell: %v; polling every half minute until it is back", err))
		}
		if time.Since(opened) > doorbellReadWithin {
			retry = time.Second // it held for a while: not a failing dial
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		retry = min(retry*2, doorbellRetryMax)
	}
}

// listenOnce holds one socket until it fails or ctx ends.
func (l *doorbellListener) listenOnce(ctx context.Context, waitURL, token string) error {
	cfg, err := websocket.NewConfig(waitURL, originFor(waitURL))
	if err != nil {
		return err
	}
	cfg.Header = http.Header{"Authorization": {"Bearer " + token}}
	dialCtx, cancelDial := context.WithTimeout(ctx, doorbellDialWithin)
	conn, err := cfg.DialContext(dialCtx)
	cancelDial()
	if err != nil {
		return err
	}
	defer conn.Close()
	l.setConnected(true)
	if l.lastFail != "" || time.Since(l.started) < doorbellReadWithin {
		l.say("waiting on the doorbell: it rings when a check is queued")
	}
	l.lastFail = ""
	stopPings := make(chan struct{})
	defer close(stopPings)
	go func() {
		ticker := time.NewTicker(doorbellPingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			case <-stopPings:
				return
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := websocket.Message.Send(conn, "ping"); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(doorbellReadWithin))
		var msg string
		if err := websocket.Message.Receive(conn, &msg); err != nil {
			return err
		}
		if msg == "wake" {
			notify(l.wake)
		}
	}
}

// originFor is the Origin header of a WebSocket dial: the same host over
// http(s).
func originFor(waitURL string) string {
	u, err := url.Parse(waitURL)
	if err != nil {
		return "https://localhost"
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path, u.RawQuery = "", ""
	return u.String()
}
