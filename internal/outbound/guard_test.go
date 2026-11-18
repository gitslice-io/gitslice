package outbound

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestValidateURL(t *testing.T) {
	for _, tc := range []struct {
		url          string
		allowPrivate bool
		ok           bool
	}{
		{"https://example.com/hook", false, true},
		{"https://example.com:8443/hook?token=x", false, true},
		{"http://example.com/hook", false, false},
		{"http://example.com/hook", true, true},
		{"ftp://example.com/hook", true, false},
		{"https://user:pass@example.com/hook", false, false},
		{"https://127.0.0.1/hook", false, false},
		{"https://127.0.0.1/hook", true, true},
		{"https://10.1.2.3/hook", false, false},
		{"https://169.254.169.254/computeMetadata", false, false},
		{"https://[::1]/hook", false, false},
		{"https://[::ffff:10.0.0.1]/hook", false, false},
		{"https://localhost/hook", false, false},
		{"https://metadata.google.internal/hook", false, false},
		{"https://100.64.0.1/hook", false, false},
		{"", false, false},
		{"not a url", false, false},
	} {
		_, err := ValidateURL(tc.url, tc.allowPrivate)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateURL(%q, %v) error = %v, want ok=%v", tc.url, tc.allowPrivate, err, tc.ok)
		}
	}
}

func TestPublicAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":           true,
		"2606:4700::1111":   true,
		"127.0.0.1":         false,
		"0.0.0.0":           false,
		"10.0.0.1":          false,
		"172.16.0.1":        false,
		"192.168.1.1":       false,
		"169.254.169.254":   false,
		"100.100.100.100":   false,
		"198.18.0.1":        false,
		"224.0.0.1":         false,
		"::1":               false,
		"fe80::1":           false,
		"fc00::1":           false,
		"::ffff:127.0.0.1":  false,
		"64:ff9b::a00:1":    false,
		"::10.0.0.1":        false, // ::/96 IPv4-compatible IPv6
		"64:ff9b:1::a00:1":  false, // 64:ff9b:1::/48 local NAT64
		"2002:0a00:0001::1": false, // 2002::/16 6to4
		"2001::1":           false, // 2001::/23 Teredo / IETF
		"2001:100::1":       false, // 2001::/23
		"100::1":            false, // 100::/64 discard-only
		"ff02::1":           false, // link-local multicast
		"ff05::1":           false, // site-local multicast
		"ff0e::1":           false, // global multicast
	} {
		if got := publicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddress(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestDialerRefusesPrivateAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	_, err := newClient(false).Get(server.URL)
	if !isBlocked(err) {
		t.Fatalf("GET %s error = %v, want the address refused", server.URL, err)
	}
	if _, err := newClient(true).Get(server.URL); err != nil {
		t.Fatalf("GET with private targets allowed: %v", err)
	}
}

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"event":"ping"}`)
	// echo -n '{"event":"ping"}' | openssl dgst -sha256 -hmac secret
	want := "sha256=4f4bb3a54e99c4a20e243485229f9b08c66e09104ba6f79c23ce647242a4ce84"
	got := Sign("secret", body)
	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
	if !Verify("secret", body, got) {
		t.Fatal("Verify rejected its own signature")
	}
	if Verify("other", body, got) || Verify("secret", []byte(`{}`), got) {
		t.Fatal("Verify accepted a wrong secret or body")
	}
}

func TestPostSignedJSONAndRetryDelay(t *testing.T) {
	if got := RetryDelay(0); got != time.Minute {
		t.Fatalf("RetryDelay(0) = %v, want 1m", got)
	}
	if got := RetryDelay(1); got != time.Minute {
		t.Fatalf("RetryDelay(1) = %v, want 1m", got)
	}
	if got := RetryDelay(6); got != 12*time.Hour {
		t.Fatalf("RetryDelay(6) = %v, want 12h", got)
	}
	if got := RetryDelay(99); got != 12*time.Hour {
		t.Fatalf("RetryDelay(99) = %v, want 12h", got)
	}

	var gotSig, gotUA, gotCustom string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(SignatureHeader)
		gotUA = r.Header.Get("User-Agent")
		gotCustom = r.Header.Get("X-Custom")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted-response"))
	}))
	defer srv.Close()

	payload := []byte(`{"hello":"world"}`)
	res, err := PostSignedJSON(context.Background(), PostJSONRequest{
		Client:           newClient(true),
		URL:              srv.URL,
		Body:             payload,
		UserAgent:        "TestAgent/1.0",
		Headers:          map[string]string{"X-Custom": "yes"},
		SignSecret:       "s3cr3t",
		MaxResponseBytes: 8,
	})
	if err != nil {
		t.Fatalf("PostSignedJSON: %v", err)
	}
	if res.StatusCode != http.StatusAccepted || string(res.Body) != "accepted" {
		t.Fatalf("res = %+v, want 202 and 8-byte bounded body", res)
	}
	if !Verify("s3cr3t", payload, gotSig) || gotUA != "TestAgent/1.0" || gotCustom != "yes" || string(gotBody) != string(payload) {
		t.Fatalf("headers/body mismatch: sig=%q ua=%q custom=%q body=%q", gotSig, gotUA, gotCustom, gotBody)
	}
}
