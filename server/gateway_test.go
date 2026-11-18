package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/requestid"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/proto/core/v1/corev1connect"
	"gitslice.io/gitslice/service"
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

func TestConnectHTTPTransportPolicyAndObservabilityParity(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)

	cfg := Config{
		// Tiny Git HTTP per-IP limit must NOT throttle Connect RPCs.
		RateLimitHTTPPerIPRPS:    0.001,
		RateLimitHTTPPerIPBurst:  1,
		RateLimitPerSubjectRPS:   0.001,
		RateLimitPerSubjectBurst: 5,
	}
	limiters := newServerLimiters(cfg)
	resolveCalls := 0
	var seenReqID string
	resolve := func(ctx context.Context, token string) (string, error) {
		resolveCalls++
		seenReqID, _ = requestid.From(ctx)
		return "user_alice", nil
	}

	api := newConnectHandler(resolve, handlers, limiters)
	srv := httptest.NewServer(newHTTPHandler(api, nil, "https://gitslice.io", cfg, true, limiters))
	t.Cleanup(srv.Close)

	client := corev1connect.NewSliceServiceClient(srv.Client(), srv.URL)
	// First 5 Connect calls succeed even though RateLimitHTTPPerIPBurst == 1.
	for i := 0; i < 5; i++ {
		req := connect.NewRequest(&corev1.ListSlicesRequest{Account: "acme"})
		req.Header().Set("Authorization", "Bearer tok_1")
		req.Header().Set("Origin", "https://gitslice.io")
		resp, err := client.ListSlices(context.Background(), req)
		if err != nil {
			t.Fatalf("Connect ListSlices #%d failed: %v", i+1, err)
		}
		respReqID := resp.Header().Get("X-Request-Id")
		if respReqID == "" {
			t.Fatalf("Connect ListSlices #%d missing X-Request-Id response header", i+1)
		}
		if seenReqID != respReqID {
			t.Fatalf("resolver request ID = %q, response header = %q", seenReqID, respReqID)
		}
		if !headerListContains(resp.Header().Get("Access-Control-Expose-Headers"), "x-request-id") {
			t.Fatalf("Access-Control-Expose-Headers = %q, want X-Request-Id", resp.Header().Get("Access-Control-Expose-Headers"))
		}
	}
	if resolveCalls != 5 {
		t.Fatalf("resolveCalls = %d, want 5", resolveCalls)
	}

	// 6th Connect call from the same IP is rejected by pre-auth grpcIP BEFORE resolve runs.
	overReq := connect.NewRequest(&corev1.ListSlicesRequest{Account: "acme"})
	overReq.Header().Set("Authorization", "Bearer tok_2")
	_, err := client.ListSlices(context.Background(), overReq)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("6th Connect ListSlices code = %v, want resource_exhausted (err=%v)", connect.CodeOf(err), err)
	}
	if resolveCalls != 5 {
		t.Fatalf("resolveCalls after pre-auth rate-limit = %d, want 5", resolveCalls)
	}

	// Scrape /metrics and verify Connect HTTP populated gitslice_grpc_requests_total for both OK and ResourceExhausted.
	metricsResp, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer metricsResp.Body.Close()
	bodyBytes, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	metricsText := string(bodyBytes)
	if !strings.Contains(metricsText, `gitslice_grpc_requests_total{method="/gitslice.core.v1.SliceService/ListSlices",code="OK"}`) {
		t.Fatalf("/metrics missing Connect OK counter:\n%s", metricsText)
	}
	if !strings.Contains(metricsText, `gitslice_grpc_requests_total{method="/gitslice.core.v1.SliceService/ListSlices",code="ResourceExhausted"}`) {
		t.Fatalf("/metrics missing Connect ResourceExhausted counter:\n%s", metricsText)
	}
}

func TestConnectAnonymousFallbackRateLimiting(t *testing.T) {
	limiters := &serverLimiters{
		subject: newGRPCRateLimiter(Config{
			RateLimitPerSubjectRPS:   0.001,
			RateLimitPerSubjectBurst: 1,
		}),
	}
	handler := connectPrincipalAuthMiddlewareWithLimiters(nil, limiters)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodPost, "/gitslice.core.v1.SliceService/GetSlice", nil)
		req.Header.Set("Connect-Protocol-Version", "1")
		req.RemoteAddr = "203.0.113.42:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("anonymous call #%d status = %d, want %d", i+1, rec.Code, want)
		}
	}
}
