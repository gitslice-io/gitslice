package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestResolveDialTarget(t *testing.T) {
	t.Setenv("GS_TLS", "")
	cases := []struct {
		in       string
		wantAddr string
		wantTLS  bool
	}{
		{"127.0.0.1:50051", "127.0.0.1:50051", false},
		{"localhost:50052", "localhost:50052", false},
		{"api.gitslice.io:443", "api.gitslice.io:443", true},
		{"https://api.gitslice.io:443", "api.gitslice.io:443", true},
		{"grpcs://api.gitslice.io:50051", "api.gitslice.io:50051", true},
		{"grpc://127.0.0.1:50051", "127.0.0.1:50051", false},
		{"http://127.0.0.1:50051/", "127.0.0.1:50051", false},
	}
	for _, tc := range cases {
		gotAddr, gotTLS := resolveDialTarget(tc.in)
		if gotAddr != tc.wantAddr || gotTLS != tc.wantTLS {
			t.Errorf("resolveDialTarget(%q) = (%q, %v), want (%q, %v)", tc.in, gotAddr, gotTLS, tc.wantAddr, tc.wantTLS)
		}
	}
}

func TestResolveDialTargetGSTLSOverride(t *testing.T) {
	t.Setenv("GS_TLS", "1")
	if _, useTLS := resolveDialTarget("127.0.0.1:50051"); !useTLS {
		t.Fatalf("GS_TLS=1 should force TLS")
	}
}

func TestClerkLoginURL(t *testing.T) {
	got, err := clerkLoginURL("https://gitslice.io", "http://127.0.0.1:5051/callback", "state123")
	if err != nil {
		t.Fatalf("clerkLoginURL error: %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result not a URL: %v", err)
	}
	if parsed.Path != "/cli-login" {
		t.Errorf("path = %q, want /cli-login", parsed.Path)
	}
	q := parsed.Query()
	if q.Get("callback_url") != "http://127.0.0.1:5051/callback" {
		t.Errorf("callback_url = %q", q.Get("callback_url"))
	}
	if q.Get("state") != "state123" {
		t.Errorf("state = %q", q.Get("state"))
	}
}

func TestClerkLoginURLRejectsBadScheme(t *testing.T) {
	if _, err := clerkLoginURL("ftp://example.com", "http://127.0.0.1/cb", "s"); err == nil {
		t.Fatal("expected error for non-http(s) web-url")
	}
}

func TestClerkLoginCallbackHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ch := make(chan clerkLoginCallbackResult, 1)
		srv := httptest.NewServer(clerkLoginCallbackHandler("st", ch))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/callback?state=st&token=jwt.abc.def")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		res := <-ch
		if res.Token != "jwt.abc.def" || res.Error != "" {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("state mismatch", func(t *testing.T) {
		ch := make(chan clerkLoginCallbackResult, 1)
		srv := httptest.NewServer(clerkLoginCallbackHandler("st", ch))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/callback?state=wrong&token=x")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("missing token", func(t *testing.T) {
		ch := make(chan clerkLoginCallbackResult, 1)
		srv := httptest.NewServer(clerkLoginCallbackHandler("st", ch))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/callback?state=st")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		res := <-ch
		if res.Error == "" || !strings.Contains(res.Error, "token") {
			t.Fatalf("expected missing-token error, got %+v", res)
		}
	})

	t.Run("error param propagated", func(t *testing.T) {
		ch := make(chan clerkLoginCallbackResult, 1)
		srv := httptest.NewServer(clerkLoginCallbackHandler("st", ch))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/callback?state=st&error=access_denied")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		res := <-ch
		if res.Error != "access_denied" {
			t.Fatalf("error = %q, want access_denied", res.Error)
		}
	})
}
