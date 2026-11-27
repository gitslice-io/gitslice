package doorbell

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The same vector as doorbell/test/auth.test.ts.
func TestTokenMatchesTheWorker(t *testing.T) {
	bell, err := New("https://doorbell.example.com", "s")
	if err != nil {
		t.Fatal(err)
	}
	want := "gsdb1.agent_1.2000000000.54f58130863f0a6be9c2a336d3eb4c558e62cba8072aaa80e8c3b8d7f6429e31"
	if got := bell.Token("agent_1", time.Unix(2000000000, 0)); got != want {
		t.Fatalf("token = %s, want %s", got, want)
	}
	if got := bell.WaitURL(); got != "wss://doorbell.example.com/wait" {
		t.Fatalf("wait URL = %s", got)
	}
}

func TestRingSignsAndReadsListeners(t *testing.T) {
	var got struct {
		DaemonID string `json:"daemon_id"`
		TS       int64  `json:"ts"`
	}
	var signature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		signature = r.Header.Get("X-Gitslice-Doorbell-Signature")
		bell, _ := New("http://x", "secret")
		if r.URL.Path != "/ring" || signature != bell.mac("ring", string(body)) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"listening":2}`))
	}))
	defer srv.Close()
	bell, err := New(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	listening, err := bell.Ring(context.Background(), "agent_1", 1)
	if err != nil || listening != 2 || got.DaemonID != "agent_1" || got.TS == 0 {
		t.Fatalf("ring = %d, %v; server saw %+v", listening, err, got)
	}
	wrong, _ := New(srv.URL, "wrong")
	if _, err := wrong.Ring(context.Background(), "agent_1", 1); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("ring with the wrong secret = %v, want 401", err)
	}
	if none, err := New("", ""); none != nil || err != nil {
		t.Fatalf("no doorbell = %v, %v", none, err)
	}
	if _, err := New("ftp://doorbell.example.com", "secret"); err == nil {
		t.Fatal("non-http(s) URL succeeded")
	}
}
