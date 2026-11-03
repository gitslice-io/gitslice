package gitcompat

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestCompactEndpointNeedsTheServersSignature(t *testing.T) {
	h := NewHandler(nil, &Projector{}, nil, nil)
	post := func(ts, sig string) int {
		target := compactPath + "?" + url.Values{"account": {"acme"}, "slice": {"p"}, "ts": {ts}}.Encode()
		req := httptest.NewRequest(http.MethodPost, target, nil)
		if sig != "" {
			req.Header.Set(compactSignatureHdr, sig)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if code := post(now, "x"); code != http.StatusNotFound {
		t.Fatalf("without a key: %d, want 404", code)
	}
	key := []byte("k")
	h.SetInternalKey(key)
	if code := post(now, ""); code != http.StatusUnauthorized {
		t.Fatalf("unsigned: %d", code)
	}
	if code := post(now, compactSignature([]byte("other"), "acme", "p", now)); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", code)
	}
	old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	if code := post(old, compactSignature(key, "acme", "p", old)); code != http.StatusUnauthorized {
		t.Fatalf("stale: %d", code)
	}
}

func TestSelfCompactRequesterSignsAndWaitsUntilSent(t *testing.T) {
	key := []byte("k")
	got := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r
		_, _ = w.Write([]byte("covered\n"))
	}))
	defer srv.Close()
	SelfCompactRequester(srv.URL, key)("acme", "p")
	select {
	case r := <-got:
		q := r.URL.Query()
		if r.URL.Path != compactPath || q.Get("account") != "acme" || q.Get("slice") != "p" ||
			r.Header.Get(compactSignatureHdr) != compactSignature(key, "acme", "p", q.Get("ts")) {
			t.Fatalf("request = %s %v %v", r.URL.Path, q, r.Header)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no request")
	}
}
