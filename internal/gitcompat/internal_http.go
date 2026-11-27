package gitcompat

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The server asks itself to build covers (cover.go) and push Git mirrors
// (design/35_git_mirrors.md) so the work runs in a request of its own, which
// Cloud Run gives CPU to. Requests go straight to the service's own URL, not
// through Cloudflare, and are authenticated with an HMAC key only the server
// holds.

const (
	compactPath         = "/git/_internal/compact"
	mirrorPath          = "/git/_internal/mirror"
	compactSignatureHdr = "X-Gitslice-Internal-Signature"
	compactMaxSkew      = 5 * time.Minute
	compactTimeout      = 30 * time.Minute
	internalReqTimeout  = compactTimeout
	internalWireWait    = 5 * time.Second
)

// MirrorRunner pushes the mirror of a slice and records how it went.
type MirrorRunner func(ctx context.Context, account, slice string) error

// SetInternalKey sets the key internal requests are signed with; without one
// the internal endpoints answer 404.
func (h *Handler) SetInternalKey(key []byte) {
	h.internalKey = append([]byte(nil), key...)
}

// SetMirrorRunner sets what the mirror endpoint runs; without one (or without
// an internal key) it answers 404.
func (h *Handler) SetMirrorRunner(run MirrorRunner) {
	h.mirrorRunner = run
}

func internalSignature(key []byte, kind, account, slice, ts string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(kind + "\x00" + account + "\x00" + slice + "\x00" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) verifyInternalRequest(r *http.Request, kind string) (account, slice string, ok bool) {
	q := r.URL.Query()
	account, slice, ts := q.Get("account"), q.Get("slice"), q.Get("ts")
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || !validGitSlug(account) || !validGitSlug(slice) || time.Since(time.Unix(sent, 0)).Abs() > compactMaxSkew ||
		!hmac.Equal([]byte(internalSignature(h.internalKey, kind, account, slice, ts)), []byte(r.Header.Get(compactSignatureHdr))) {
		return "", "", false
	}
	return account, slice, true
}

// serveCompact builds a cover for the slice named in the query, for the
// server itself. It answers when the cover is in the object store.
func (h *Handler) serveCompact(w http.ResponseWriter, r *http.Request) {
	if len(h.internalKey) == 0 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	account, slice, ok := h.verifyInternalRequest(r, "compact")
	if !ok {
		http.Error(w, "bad internal request", http.StatusUnauthorized)
		return
	}
	if err := h.projector.Compact(r.Context(), account, slice); err != nil {
		slog.Warn("git cover failed", "repo", account+"/"+slice+".git", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(w, "covered\n")
}

func (h *Handler) serveMirror(w http.ResponseWriter, r *http.Request) {
	if len(h.internalKey) == 0 || h.mirrorRunner == nil || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	account, slice, ok := h.verifyInternalRequest(r, "mirror")
	if !ok {
		http.Error(w, "bad internal request", http.StatusUnauthorized)
		return
	}
	if err := h.mirrorRunner(r.Context(), account, slice); err != nil {
		// The runner recorded the failure on the mirror; this is for the logs.
		http.Error(w, "mirror push failed", http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(w, "mirrored\n")
}

func newSelfInternalRequester(
	baseURL, endpoint, kind string,
	key []byte,
	onErr func(account, slice string, err error),
	onStatus func(account, slice string, status int, body string),
) func(account, slice string) {
	baseURL = strings.TrimRight(baseURL, "/")
	keyCopy := append([]byte(nil), key...)
	client := &http.Client{Timeout: internalReqTimeout}
	return func(account, slice string) {
		// The caller's request is about to end, and with it this container's
		// CPU: wait (briefly) until the request is on the wire.
		sent := make(chan struct{})
		var once sync.Once
		markSent := func() { once.Do(func() { close(sent) }) }
		go func() {
			defer markSent()
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			target := baseURL + endpoint + "?" + url.Values{"account": {account}, "slice": {slice}, "ts": {ts}}.Encode()
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
				WroteRequest: func(httptrace.WroteRequestInfo) { markSent() },
			})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(nil))
			if err != nil {
				return
			}
			req.Header.Set(compactSignatureHdr, internalSignature(keyCopy, kind, account, slice, ts))
			resp, err := client.Do(req)
			if err != nil {
				if onErr != nil {
					onErr(account, slice, err)
				}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK && onStatus != nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				onStatus(account, slice, resp.StatusCode, strings.TrimSpace(string(body)))
			}
		}()
		timer := time.NewTimer(internalWireWait)
		defer timer.Stop()
		select {
		case <-sent:
		case <-timer.C:
		}
	}
}

// SelfCompactRequester returns a CompactRequester that asks the server at
// baseURL (its own Cloud Run URL) to build a cover, signed with key.
func SelfCompactRequester(baseURL string, key []byte) CompactRequester {
	return newSelfInternalRequester(
		baseURL, compactPath, "compact", key,
		func(account, slice string, err error) {
			slog.Warn("could not ask for a git cover", "repo", account+"/"+slice+".git", "error", err)
		},
		func(account, slice string, status int, body string) {
			slog.Warn("git cover request failed", "repo", account+"/"+slice+".git", "status", status, "body", body)
		},
	)
}

// SelfMirrorRequester returns a function that asks the server at baseURL (its
// own Cloud Run URL) to push a slice's mirror, signed with key. It returns
// once the request is on the wire.
func SelfMirrorRequester(baseURL string, key []byte) func(account, slice string) {
	return newSelfInternalRequester(
		baseURL, mirrorPath, "mirror", key,
		func(account, slice string, err error) {
			slog.Warn("could not ask for a git mirror push", "repo", account+"/"+slice, "error", err)
		},
		nil,
	)
}

// compactURLPath reports whether the request is for the compaction endpoint.
func compactURLPath(path string) bool {
	return path == compactPath
}
