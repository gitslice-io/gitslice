package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPHandlerCORSAllowsConnectTimeoutMs(t *testing.T) {
	handler := NewHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight should not reach wrapped handler")
	}), nil, "https://gitslice.io", Config{RateLimitDisabled: true})

	req := httptest.NewRequest(http.MethodOptions, "/gitslice.core.v1.ChangesetService/SubmitChangeset", nil)
	req.Header.Set("Origin", "https://gitslice.io")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization,connect-protocol-version,connect-timeout-ms,content-type,x-user-agent")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{
		"authorization",
		"connect-protocol-version",
		"connect-timeout-ms",
		"content-type",
		"x-user-agent",
	} {
		if !headerListContains(allowHeaders, header) {
			t.Fatalf("Access-Control-Allow-Headers = %q, missing %q", allowHeaders, header)
		}
	}
}

func headerListContains(values, want string) bool {
	want = strings.ToLower(want)
	for _, value := range strings.Split(values, ",") {
		if strings.ToLower(strings.TrimSpace(value)) == want {
			return true
		}
	}
	return false
}

func TestHealthzAndReadyzProbes(t *testing.T) {
	handler := NewHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}), nil, "", Config{
		RateLimitHTTPPerIPRPS:   0.001,
		RateLimitHTTPPerIPBurst: 1,
	})

	for _, path := range []string{"/healthz", "/readyz"} {
		// Multiple GET/HEAD probes from the same IP should never be rate-limited.
		for i := 0; i < 4; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "203.0.113.9:1234"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
				t.Fatalf("GET %s #%d = (%d, %q), want (200, \"ok\\n\")", path, i, rec.Code, rec.Body.String())
			}
		}

		headReq := httptest.NewRequest(http.MethodHead, path, nil)
		headReq.RemoteAddr = "203.0.113.9:1234"
		headRec := httptest.NewRecorder()
		handler.ServeHTTP(headRec, headReq)
		if headRec.Code != http.StatusOK || headRec.Body.Len() != 0 {
			t.Fatalf("HEAD %s = (%d, len=%d), want (200, len=0)", path, headRec.Code, headRec.Body.Len())
		}

		postReq := httptest.NewRequest(http.MethodPost, path, nil)
		postRec := httptest.NewRecorder()
		handler.ServeHTTP(postRec, postReq)
		if postRec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want %d", path, postRec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestMetricsRateLimitingAndConstantTimeTokenAuth(t *testing.T) {
	handler := NewHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}), nil, "", Config{
		MetricsToken:            "super-secret-metrics-token",
		RateLimitHTTPPerIPRPS:   0.001,
		RateLimitHTTPPerIPBurst: 3,
	})

	// Wrong-length and wrong-value tokens fail with 401 + WWW-Authenticate.
	for _, hdr := range []string{"", "Bearer short", "Bearer super-secret-metrics-tokeX"} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "203.0.113.77:1000"
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization=%q status = %d, want 401", hdr, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
			t.Fatalf("WWW-Authenticate = %q, want Bearer challenge", got)
		}
	}

	// Fourth request from the same IP exceeds burst=3 on /metrics even before token check.
	overReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	overReq.RemoteAddr = "203.0.113.77:1000"
	overReq.Header.Set("Authorization", "Bearer super-secret-metrics-token")
	overRec := httptest.NewRecorder()
	handler.ServeHTTP(overRec, overReq)
	if overRec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th /metrics request status = %d, want 429", overRec.Code)
	}

	// Fresh IP succeeds with Bearer and ?token=.
	okReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	okReq.RemoteAddr = "203.0.113.78:1000"
	okReq.Header.Set("Authorization", "Bearer super-secret-metrics-token")
	okRec := httptest.NewRecorder()
	handler.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("valid Bearer /metrics status = %d, want 200", okRec.Code)
	}

	qReq := httptest.NewRequest(http.MethodGet, "/metrics?token=super-secret-metrics-token", nil)
	qReq.RemoteAddr = "203.0.113.78:1000"
	qRec := httptest.NewRecorder()
	handler.ServeHTTP(qRec, qReq)
	if qRec.Code != http.StatusOK {
		t.Fatalf("valid query-token /metrics status = %d, want 200", qRec.Code)
	}
}
