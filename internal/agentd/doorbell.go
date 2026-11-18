package agentd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// The doorbell (design/32_ci_dispatch_on_cloudflare.md, layer 1): a
// detached checks-only daemon holds a WebSocket open to a Cloudflare Worker,
// which says "wake" when the server queues a check for it.
const (
	DoorbellPingEvery        = 30 * time.Second
	DoorbellReadWithin       = 75 * time.Second
	DoorbellDialWithin       = 15 * time.Second
	DoorbellRetryMax         = time.Minute
	DoorbellInitialReadyWait = 2 * time.Second
)

type DoorbellListener struct {
	mu          sync.Mutex
	url         string
	token       string
	requireTLS  bool
	insecureCtx func(context.Context) bool
	connected   bool
	say         func(string)
	lastFail    string
	started     time.Time

	ready     chan struct{}
	readyOnce sync.Once
	wake      chan struct{}
	lost      chan struct{}
	cancel    context.CancelFunc
	done      chan struct{}
}

// ListenDoorbell starts waiting on the doorbell at waitURL with token.
func ListenDoorbell(ctx context.Context, waitURL, token string, say func(string)) *DoorbellListener {
	return ListenDoorbellWithTLS(ctx, waitURL, token, false, nil, say)
}

func ListenDoorbellWithTLS(ctx context.Context, waitURL, token string, requireTLS bool, insecureCtx func(context.Context) bool, say func(string)) *DoorbellListener {
	ctx, cancel := context.WithCancel(ctx)
	if say == nil {
		say = func(string) {}
	}
	l := &DoorbellListener{
		say:         say,
		url:         waitURL,
		token:       token,
		requireTLS:  requireTLS,
		insecureCtx: insecureCtx,
		ready:       make(chan struct{}),
		wake:        make(chan struct{}, 1),
		lost:        make(chan struct{}, 1),
		cancel:      cancel,
		done:        make(chan struct{}),
		started:     time.Now(),
	}
	go l.run(ctx)
	return l
}

// Update gives the listener a fresh token (and URL) for its next dial.
func (l *DoorbellListener) Update(waitURL, token string) {
	l.UpdateWithTLS(waitURL, token, false)
}

func (l *DoorbellListener) UpdateWithTLS(waitURL, token string, requireTLS bool) {
	l.mu.Lock()
	l.url, l.token, l.requireTLS = waitURL, token, requireTLS
	l.mu.Unlock()
}

func (l *DoorbellListener) markReady() {
	if l == nil {
		return
	}
	l.readyOnce.Do(func() {
		close(l.ready)
	})
}

func (l *DoorbellListener) WaitReady(ctx context.Context, timeout time.Duration) {
	if l == nil || timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-l.ready:
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (l *DoorbellListener) Stop() {
	if l == nil {
		return
	}
	l.cancel()
	<-l.done
}

// IsConnected reports whether the socket is open.
func (l *DoorbellListener) IsConnected() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connected
}

func (l *DoorbellListener) Woken() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.wake
}

func (l *DoorbellListener) Dropped() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.lost
}

func NotifyChan(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (l *DoorbellListener) setConnected(connected bool) {
	l.mu.Lock()
	was := l.connected
	l.connected = connected
	l.mu.Unlock()
	if connected {
		l.markReady()
	}
	if was && !connected {
		NotifyChan(l.lost)
	}
}

func (l *DoorbellListener) run(ctx context.Context) {
	defer close(l.done)
	defer l.markReady()
	retry := time.Second
	for ctx.Err() == nil {
		l.mu.Lock()
		waitURL, token, requireTLS := l.url, l.token, l.requireTLS
		l.mu.Unlock()
		opened := time.Now()
		err := l.listenOnce(ctx, waitURL, token, requireTLS)
		l.setConnected(false)
		l.markReady()
		if ctx.Err() != nil {
			return
		}
		if why := fmt.Sprint(err); why != l.lastFail {
			l.lastFail = why
			l.say(fmt.Sprintf("doorbell: %v; polling every half minute until it is back", err))
		}
		if time.Since(opened) > DoorbellReadWithin {
			retry = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		retry = min(retry*2, DoorbellRetryMax)
	}
}

func (l *DoorbellListener) dialDoorbell(ctx context.Context, waitURL, token string, requireTLS bool) (*websocket.Conn, error) {
	insecureReq := false
	if l != nil && l.insecureCtx != nil {
		insecureReq = l.insecureCtx(ctx)
	}
	normalized, err := NormalizeDoorbellURL(waitURL, requireTLS, insecureReq)
	if err != nil {
		return nil, err
	}
	cfg, err := websocket.NewConfig(normalized, OriginForDoorbell(normalized))
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(normalized, "wss://") {
		u, _ := url.Parse(normalized)
		cfg.TlsConfig = &tls.Config{
			ServerName: u.Hostname(),
			MinVersion: tls.VersionTLS12,
		}
	}
	cfg.Header = http.Header{"Authorization": {"Bearer " + token}}
	dialCtx, cancelDial := context.WithTimeout(ctx, DoorbellDialWithin)
	defer cancelDial()
	return cfg.DialContext(dialCtx)
}

func NormalizeDoorbellURL(waitURL string, requireTLS, insecureRequested bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(waitURL))
	if err != nil {
		return "", fmt.Errorf("invalid doorbell URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "wss", "https":
		u.Scheme = "wss"
	case "ws", "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported doorbell scheme %q", u.Scheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("doorbell URL missing host")
	}
	mustTLS := requireTLS || (!isLoopbackHost(u.Hostname()) && !insecureTransportRequested(insecureRequested))
	if mustTLS && u.Scheme != "wss" {
		return "", fmt.Errorf("doorbell URL must use wss:// for non-loopback TLS target")
	}
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func insecureTransportRequested(explicit bool) bool {
	if explicit {
		return true
	}
	for _, key := range []string{"GS_INSECURE", "GITSLICE_INSECURE"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

func (l *DoorbellListener) listenOnce(ctx context.Context, waitURL, token string, requireTLS bool) error {
	conn, err := l.dialDoorbell(ctx, waitURL, token, requireTLS)
	if err != nil {
		return err
	}
	defer conn.Close()
	l.setConnected(true)
	if l.lastFail != "" || time.Since(l.started) < DoorbellReadWithin {
		l.say("waiting on the doorbell: it rings when a check is queued")
	}
	l.lastFail = ""
	stopPings := make(chan struct{})
	defer close(stopPings)
	go func() {
		ticker := time.NewTicker(DoorbellPingEvery)
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
		_ = conn.SetReadDeadline(time.Now().Add(DoorbellReadWithin))
		var msg string
		if err := websocket.Message.Receive(conn, &msg); err != nil {
			return err
		}
		if msg == "wake" {
			NotifyChan(l.wake)
		}
	}
}

// OriginForDoorbell is the Origin header of a WebSocket dial: the same host over http(s).
func OriginForDoorbell(waitURL string) string {
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

// DoorbellDisabled reports whether GITSLICE_AGENT_DOORBELL=off is set.
func DoorbellDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("GITSLICE_AGENT_DOORBELL")), "off")
}
