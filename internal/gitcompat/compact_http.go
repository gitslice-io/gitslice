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

// The server asks itself to build covers (cover.go) so the work runs in a
// request of its own, which Cloud Run gives CPU to. The request goes straight
// to the service's own URL, not through Cloudflare, whose 100-second limit a
// cover of a large slice can outlast, and is signed with a key only the
// server holds.

const (
	compactPath         = "/git/_internal/compact"
	compactSignatureHdr = "X-Gitslice-Internal-Signature"
	compactMaxSkew      = 5 * time.Minute
	compactTimeout      = 30 * time.Minute
)

// SetInternalKey sets the key internal requests are signed with; without one
// the compaction endpoint answers 404.
func (h *Handler) SetInternalKey(key []byte) {
	h.internalKey = append([]byte(nil), key...)
}

func compactSignature(key []byte, account, slice, ts string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("compact\x00" + account + "\x00" + slice + "\x00" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

// serveCompact builds a cover for the slice named in the query, for the
// server itself. It answers when the cover is in the object store.
func (h *Handler) serveCompact(w http.ResponseWriter, r *http.Request) {
	if len(h.internalKey) == 0 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	account, slice, ts := q.Get("account"), q.Get("slice"), q.Get("ts")
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || account == "" || slice == "" || time.Since(time.Unix(sent, 0)).Abs() > compactMaxSkew ||
		!hmac.Equal([]byte(compactSignature(h.internalKey, account, slice, ts)), []byte(r.Header.Get(compactSignatureHdr))) {
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

// SelfCompactRequester returns a CompactRequester that asks the server at
// baseURL (its own Cloud Run URL) to build a cover, signed with key.
func SelfCompactRequester(baseURL string, key []byte) CompactRequester {
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: compactTimeout}
	return func(account, slice string) {
		// The caller's request is about to end, and with it this container's
		// CPU: wait (briefly) until the request is on the wire.
		sent := make(chan struct{})
		var once sync.Once
		markSent := func() { once.Do(func() { close(sent) }) }
		go func() {
			defer markSent()
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			target := baseURL + compactPath + "?" + url.Values{"account": {account}, "slice": {slice}, "ts": {ts}}.Encode()
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
				WroteRequest: func(httptrace.WroteRequestInfo) { markSent() },
			})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(nil))
			if err != nil {
				return
			}
			req.Header.Set(compactSignatureHdr, compactSignature(key, account, slice, ts))
			resp, err := client.Do(req)
			if err != nil {
				slog.Warn("could not ask for a git cover", "repo", account+"/"+slice+".git", "error", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				slog.Warn("git cover request failed", "repo", account+"/"+slice+".git", "status", resp.StatusCode, "body", strings.TrimSpace(string(body)))
			}
		}()
		select {
		case <-sent:
		case <-time.After(5 * time.Second):
		}
	}
}

// compactURLPath reports whether the request is for the compaction endpoint.
func compactURLPath(path string) bool {
	return path == compactPath
}
