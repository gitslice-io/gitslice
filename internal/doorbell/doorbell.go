// Package doorbell signs tokens for detached CI daemons and rings the
// Cloudflare Worker doorbell (design/32_ci_dispatch_on_cloudflare.md, layer 1).
package doorbell

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// SafetyPoll is how often a daemon on the doorbell polls anyway.
	// Each poll wakes the server and its database, so it is long: a ring is
	// tried three times and kept for a daemon that is reconnecting.
	SafetyPoll = time.Hour
	// StandbyGrace is how long past a missed safety poll a daemon still
	// counts as online.
	StandbyGrace = 3 * time.Minute
	// TokenLife is how long a doorbell token is good for; a safety
	// poll hands out a fresh one long before it runs out.
	TokenLife = 3 * time.Hour
)

// Client signs tokens for daemons and rings the doorbell Worker. The
// server and the Worker share secret.
type Client struct {
	base   *url.URL
	secret []byte
	client *http.Client
}

// New returns a doorbell client for the Worker at baseURL (http or https), or
// nil without both a URL and secret.
func New(baseURL, secret string) (*Client, error) {
	baseURL, secret = strings.TrimSpace(baseURL), strings.TrimSpace(secret)
	if baseURL == "" || secret == "" {
		return nil, nil
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("doorbell URL %q is not an http(s) URL", baseURL)
	}
	return &Client{base: parsed, secret: []byte(secret), client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (c *Client) mac(parts ...string) string {
	h := hmac.New(sha256.New, c.secret)
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
func (c *Client) Token(daemonID string, expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return "gsdb1." + daemonID + "." + exp + "." + c.mac("doorbell-token", daemonID, exp)
}

// WaitURL is the WebSocket a daemon opens to wait.
func (c *Client) WaitURL() string {
	u := *c.base
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
func (c *Client) Ring(ctx context.Context, daemonID string, queued int) (int, error) {
	body, err := json.Marshal(map[string]any{"daemon_id": daemonID, "queued": queued, "ts": time.Now().Unix()})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.String()+"/ring", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitslice-Doorbell-Signature", c.mac("ring", string(body)))
	resp, err := c.client.Do(req)
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
