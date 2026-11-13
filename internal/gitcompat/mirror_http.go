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

// Git mirror pushes (design/35_git_mirrors.md) run, like covers, in a request
// the server sends itself, so Cloud Run gives them CPU. The request names the
// slice only; the runner reads the mirror's setting and records the outcome.

const (
	mirrorPath    = "/git/_internal/mirror"
	mirrorTimeout = 30 * time.Minute
)

// MirrorRunner pushes the mirror of a slice and records how it went.
type MirrorRunner func(ctx context.Context, account, slice string) error

// SetMirrorRunner sets what the mirror endpoint runs; without one (or
// without an internal key) it answers 404.
func (h *Handler) SetMirrorRunner(run MirrorRunner) {
	h.mirrorRunner = run
}

func mirrorSignature(key []byte, account, slice, ts string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("mirror\x00" + account + "\x00" + slice + "\x00" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) serveMirror(w http.ResponseWriter, r *http.Request) {
	if len(h.internalKey) == 0 || h.mirrorRunner == nil || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	account, slice, ts := q.Get("account"), q.Get("slice"), q.Get("ts")
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || account == "" || slice == "" || time.Since(time.Unix(sent, 0)).Abs() > compactMaxSkew ||
		!hmac.Equal([]byte(mirrorSignature(h.internalKey, account, slice, ts)), []byte(r.Header.Get(compactSignatureHdr))) {
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

// SelfMirrorRequester returns a function that asks the server at baseURL (its
// own Cloud Run URL) to push a slice's mirror, signed with key. It returns
// once the request is on the wire.
func SelfMirrorRequester(baseURL string, key []byte) func(account, slice string) {
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: mirrorTimeout}
	return func(account, slice string) {
		sent := make(chan struct{})
		var once sync.Once
		markSent := func() { once.Do(func() { close(sent) }) }
		go func() {
			defer markSent()
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			target := baseURL + mirrorPath + "?" + url.Values{"account": {account}, "slice": {slice}, "ts": {ts}}.Encode()
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
				WroteRequest: func(httptrace.WroteRequestInfo) { markSent() },
			})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(nil))
			if err != nil {
				return
			}
			req.Header.Set(compactSignatureHdr, mirrorSignature(key, account, slice, ts))
			resp, err := client.Do(req)
			if err != nil {
				slog.Warn("could not ask for a git mirror push", "repo", account+"/"+slice, "error", err)
				return
			}
			_ = resp.Body.Close()
		}()
		select {
		case <-sent:
		case <-time.After(5 * time.Second):
		}
	}
}
