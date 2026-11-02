package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The doorbell (design/32_ci_dispatch_on_cloudflare.md, layer 1): a
// Cloudflare Worker (doorbell/) where a detached CI daemon holds a WebSocket
// open, and which the server rings when a check is queued for it. The daemon
// then polls only every doorbellSafetyPoll, so an idle server and database
// can scale to zero. The database stays the record of every run: a lost ring
// costs a late start, never a lost check.

const (
	// doorbellSafetyPoll is how often a daemon on the doorbell polls anyway.
	// Each poll wakes the server and its database, so it is long: a ring is
	// tried three times and kept for a daemon that is reconnecting.
	doorbellSafetyPoll = time.Hour
	// doorbellStandbyGrace is how long past a missed safety poll it still
	// counts as online.
	doorbellStandbyGrace = 3 * time.Minute
	// doorbellTokenLife is how long a doorbell token is good for; a safety
	// poll hands out a fresh one long before it runs out.
	doorbellTokenLife = 3 * time.Hour
)

// Doorbell signs tokens for daemons and rings the doorbell Worker. The
// server and the Worker share secret.
type Doorbell struct {
	base   *url.URL
	secret []byte
	client *http.Client
}

// NewDoorbell returns a doorbell for the Worker at baseURL (https), or nil
// without a URL and secret.
func NewDoorbell(baseURL, secret string) (*Doorbell, error) {
	baseURL, secret = strings.TrimSpace(baseURL), strings.TrimSpace(secret)
	if baseURL == "" || secret == "" {
		return nil, nil
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("doorbell URL %q is not an http(s) URL", baseURL)
	}
	return &Doorbell{base: parsed, secret: []byte(secret), client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (b *Doorbell) mac(parts ...string) string {
	h := hmac.New(sha256.New, b.secret)
	for i, part := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Token is what a daemon presents to wait on the doorbell:
// gsdb1.<daemon>.<expiry unix>.<HMAC("doorbell-token", daemon, expiry)>.
// It can only receive the daemon's own wake-ups.
func (b *Doorbell) Token(daemonID string, expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return "gsdb1." + daemonID + "." + exp + "." + b.mac("doorbell-token", daemonID, exp)
}

// WaitURL is the WebSocket a daemon opens to wait.
func (b *Doorbell) WaitURL() string {
	u := *b.base
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/wait"
	return u.String()
}

// Ring tells the daemon's waiting processes that queued check runs wait for
// them (for Cloudflare runners, the doorbell starts up to queued of them).
// It returns how many were listening or started; with none, the doorbell
// keeps the ring for the daemon's next wait.
func (b *Doorbell) Ring(ctx context.Context, daemonID string, queued int) (int, error) {
	body, err := json.Marshal(map[string]any{"daemon_id": daemonID, "queued": queued, "ts": time.Now().Unix()})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base.String()+"/ring", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitslice-Doorbell-Signature", b.mac("ring", string(body)))
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("doorbell answered %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Listening int `json:"listening"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, fmt.Errorf("doorbell answer: %w", err)
	}
	return out.Listening, nil
}

// ringDaemon rings for the daemon in the background, trying a few times. A
// daemon nobody hears loses its standby, so it counts as offline and its
// queued runs can move to another daemon of the pool.
func (s *AgentService) ringDaemon(ctx context.Context, daemonID string, queued int) {
	if s.doorbell == nil || daemonID == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		var err error
		for attempt, wait := range []time.Duration{0, time.Second, 4 * time.Second} {
			if wait > 0 {
				time.Sleep(wait)
			}
			var listening int
			if listening, err = s.doorbell.Ring(ctx, daemonID, queued); err == nil {
				if listening == 0 && len(s.hub.conns(daemonID)) == 0 && !s.dispatcher.onDemand[daemonID] {
					_ = s.Agents.SetDaemonStandby(ctx, daemonID, time.Time{})
				}
				return
			}
			slog.Debug("doorbell ring failed", "daemon_id", daemonID, "attempt", attempt+1, "error", err)
		}
		slog.Warn("could not ring the doorbell; the daemon finds its runs at its next poll", "daemon_id", daemonID, "error", err)
	}()
}

// SetCIRunners names the daemons whose processes the doorbell starts on
// demand (Cloudflare runners, design/32 layer 2).
func (h *Handlers) SetCIRunners(daemonIDs []string) {
	if h.Agent.dispatcher == nil {
		return
	}
	h.Agent.dispatcher.onDemand = map[string]bool{}
	for _, id := range daemonIDs {
		if id = strings.TrimSpace(id); id != "" {
			h.Agent.dispatcher.onDemand[id] = true
		}
	}
}

// SetDoorbell gives the server a doorbell (nil for none).
func (h *Handlers) SetDoorbell(doorbell *Doorbell) {
	h.Agent.doorbell = doorbell
	if h.Agent.dispatcher == nil {
		return
	}
	if doorbell == nil {
		h.Agent.dispatcher.wake = nil
		return
	}
	h.Agent.dispatcher.wake = h.Agent.ringDaemon
}
