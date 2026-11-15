package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage/memory"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/proto/core/v1/corev1connect"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestGRPCRateLimitUnaryUsesSubjectID(t *testing.T) {
	limiter := newGRPCRateLimiter(Config{
		RateLimitPerSubjectRPS:   1,
		RateLimitPerSubjectBurst: 1,
	})
	interceptor := grpcRateLimitUnaryInterceptor(limiter)
	info := &grpc.UnaryServerInfo{FullMethod: "/gitslice.core.v1.SliceService/GetSlice"}
	calls := 0
	handler := func(ctx context.Context, req any) (any, error) {
		calls++
		return "ok", nil
	}

	alice := authctx.WithSubjectID(context.Background(), "alice")
	if _, err := interceptor(alice, nil, info, handler); err != nil {
		t.Fatalf("first alice request failed: %v", err)
	}
	if _, err := interceptor(alice, nil, info, handler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second alice request code = %v, want ResourceExhausted", status.Code(err))
	}
	bob := authctx.WithSubjectID(context.Background(), "bob")
	if _, err := interceptor(bob, nil, info, handler); err != nil {
		t.Fatalf("first bob request failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2", calls)
	}
}

func TestGRPCRateLimitUnaryFallsBackToPeerIP(t *testing.T) {
	limiter := newGRPCRateLimiter(Config{
		RateLimitPerSubjectRPS:   1,
		RateLimitPerSubjectBurst: 1,
	})
	interceptor := grpcRateLimitUnaryInterceptor(limiter)
	info := &grpc.UnaryServerInfo{FullMethod: "/gitslice.core.v1.AuthService/StartCliLogin"}
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 1234}})

	if _, err := interceptor(ctx, nil, info, handler); err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	if _, err := interceptor(ctx, nil, info, handler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second request code = %v, want ResourceExhausted", status.Code(err))
	}
}

func TestGRPCRateLimitExemptsHealthChecks(t *testing.T) {
	limiter := newGRPCRateLimiter(Config{
		RateLimitPerSubjectRPS:   1,
		RateLimitPerSubjectBurst: 1,
	})
	interceptor := grpcRateLimitUnaryInterceptor(limiter)
	info := &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	for i := 0; i < 5; i++ {
		if _, err := interceptor(context.Background(), nil, info, handler); err != nil {
			t.Fatalf("health request %d failed: %v", i, err)
		}
	}
}

func TestGRPCRateLimitStreamRejectsOverLimit(t *testing.T) {
	limiter := newGRPCRateLimiter(Config{
		RateLimitPerSubjectRPS:   1,
		RateLimitPerSubjectBurst: 1,
	})
	interceptor := grpcRateLimitStreamInterceptor(limiter)
	info := &grpc.StreamServerInfo{FullMethod: "/gitslice.core.v1.BlobService/UploadBlob"}
	stream := &testServerStream{ctx: authctx.WithSubjectID(context.Background(), "alice")}
	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	if err := interceptor(nil, stream, info, handler); err != nil {
		t.Fatalf("first stream failed: %v", err)
	}
	if err := interceptor(nil, stream, info, handler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second stream code = %v, want ResourceExhausted", status.Code(err))
	}
}

func TestHTTPRateLimitUsesForwardedForRightmostHop(t *testing.T) {
	handler := newHTTPRateLimitMiddleware(Config{
		RateLimitHTTPPerIPRPS:   1,
		RateLimitHTTPPerIPBurst: 1,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.1")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("first status = %d, want %d", res.Code, http.StatusNoContent)
	}

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req.Clone(context.Background()))
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.11, 198.51.100.1")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("different attacker-controlled hop status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.11, 198.51.100.2")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("different rightmost hop status = %d, want %d", res.Code, http.StatusNoContent)
	}
}

func TestHTTPClientIP(t *testing.T) {
	tests := []struct {
		name       string
		forwarded  string
		remoteAddr string
		want       string
	}{
		{
			name:       "rightmost entry wins with multiple hops from trusted proxy",
			forwarded:  "1.1.1.1, 2.2.2.2, 3.3.3.3",
			remoteAddr: "127.0.0.1:1234",
			want:       "3.3.3.3",
		},
		{
			name:       "single entry from private proxy",
			forwarded:  "1.1.1.1",
			remoteAddr: "10.0.0.50:1234",
			want:       "1.1.1.1",
		},
		{
			name:       "spaces are trimmed from link-local proxy",
			forwarded:  " 1.1.1.1 ,  2.2.2.2  ",
			remoteAddr: "169.254.1.10:1234",
			want:       "2.2.2.2",
		},
		{
			name:       "untrusted public peer ignores spoofed X-Forwarded-For",
			forwarded:  "1.1.1.1, 2.2.2.2",
			remoteAddr: "198.51.100.50:1234",
			want:       "198.51.100.50",
		},
		{
			name:       "empty header falls back to remote address",
			remoteAddr: "198.51.100.50:1234",
			want:       "198.51.100.50",
		},
		{
			name:       "trailing comma uses nearest non-empty entry",
			forwarded:  "1.1.1.1, 2.2.2.2, ",
			remoteAddr: "127.0.0.1:1234",
			want:       "2.2.2.2",
		},
		{
			name:       "multiple empty rightmost segments are skipped",
			forwarded:  "1.1.1.1, 2.2.2.2, ,  ",
			remoteAddr: "127.0.0.1:1234",
			want:       "2.2.2.2",
		},
		{
			name:      "empty forwarded entries and remote address return unknown",
			forwarded: ", ,",
			want:      "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tt.forwarded)
			}

			if got := httpClientIP(req); got != tt.want {
				t.Fatalf("httpClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUntrustedProxyIgnoresForwardedForAndTrustedProxyCIDRHonorsIt(t *testing.T) {
	// Default: public peer 198.51.100.50 cannot spoof X-Forwarded-For on HTTP or gRPC.
	untrusted := newHTTPRateLimitMiddleware(Config{
		RateLimitHTTPPerIPRPS:   0.001,
		RateLimitHTTPPerIPBurst: 1,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "198.51.100.50:1111"
	req1.Header.Set("X-Forwarded-For", "203.0.113.1")
	rec1 := httptest.NewRecorder()
	untrusted.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("first status = %d, want 204", rec1.Code)
	}

	// Attacker rotates X-Forwarded-For on second request from same public peer; still throttled.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "198.51.100.50:2222"
	req2.Header.Set("X-Forwarded-For", "203.0.113.2")
	rec2 := httptest.NewRecorder()
	untrusted.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second status with spoofed XFF = %d, want 429", rec2.Code)
	}

	// Same check for gRPC metadata x-forwarded-for.
	grpcCtx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("198.51.100.50"), Port: 4444},
	})
	grpcCtx = metadata.NewIncomingContext(grpcCtx, metadata.Pairs("x-forwarded-for", "203.0.113.99"))
	if got := grpcPeerIPKey(grpcCtx); got != "ip:198.51.100.50" {
		t.Fatalf("grpcPeerIPKey(untrusted) = %q, want ip:198.51.100.50", got)
	}

	// Explicit TrustedProxyCIDRs trusts 198.51.100.0/24 and rejects loopback not in list.
	proxies, err := parseTrustedProxies([]string{"198.51.100.0/24"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	if got := grpcPeerIPKeyWithProxies(grpcCtx, proxies); got != "ip:203.0.113.99" {
		t.Fatalf("grpcPeerIPKeyWithProxies(explicit) = %q, want ip:203.0.113.99", got)
	}
	loopReq := httptest.NewRequest(http.MethodGet, "/", nil)
	loopReq.RemoteAddr = "127.0.0.1:9999"
	loopReq.Header.Set("X-Forwarded-For", "203.0.113.88")
	if got := httpClientIPWithProxies(loopReq, proxies); got != "127.0.0.1" {
		t.Fatalf("httpClientIPWithProxies(unlisted loopback) = %q, want 127.0.0.1", got)
	}
}

func TestIPv6Subnet64Bucketing(t *testing.T) {
	handler := newHTTPRateLimitMiddleware(Config{
		RateLimitHTTPPerIPRPS:   0.001,
		RateLimitHTTPPerIPBurst: 1,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	reqA := httptest.NewRequest(http.MethodGet, "/", nil)
	reqA.RemoteAddr = "[2001:db8:1:2::1]:1234"
	recA := httptest.NewRecorder()
	handler.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusNoContent {
		t.Fatalf("first /64 host status = %d, want 204", recA.Code)
	}

	// Distinct host in same /64 shares the bucket.
	reqB := httptest.NewRequest(http.MethodGet, "/", nil)
	reqB.RemoteAddr = "[2001:db8:1:2::ffff]:5678"
	recB := httptest.NewRecorder()
	handler.ServeHTTP(recB, reqB)
	if recB.Code != http.StatusTooManyRequests {
		t.Fatalf("second host in same /64 status = %d, want 429", recB.Code)
	}

	// Different /64 gets its own bucket.
	reqC := httptest.NewRequest(http.MethodGet, "/", nil)
	reqC.RemoteAddr = "[2001:db8:1:3::1]:1234"
	recC := httptest.NewRecorder()
	handler.ServeHTTP(recC, reqC)
	if recC.Code != http.StatusNoContent {
		t.Fatalf("different /64 status = %d, want 204", recC.Code)
	}
}

func TestGRPCServerPreAuthIPRateLimitRunsBeforeSubjectResolver(t *testing.T) {
	mem := memory.New()
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)

	resolveCalls := 0
	resolve := func(ctx context.Context, token string) (string, error) {
		resolveCalls++
		return "user_alice", nil
	}
	// gRPC's pre-auth per-IP limit uses the per-subject rate (not the Git
	// HTTP rate), so the subject rate is what this test makes tiny.
	grpcSrv := NewGRPCServer(resolve, handlers, Config{
		RateLimitHTTPPerIPRPS:    100,
		RateLimitHTTPPerIPBurst:  100,
		RateLimitPerSubjectRPS:   0.001,
		RateLimitPerSubjectBurst: 1,
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(grpcSrv.Stop)
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := corev1.NewSliceServiceClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer tok_1")
	if _, err := client.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme"}); err != nil && status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("first ListSlices unexpectedly rate-limited: %v", err)
	}
	if resolveCalls != 1 {
		t.Fatalf("resolveCalls after first RPC = %d, want 1", resolveCalls)
	}

	// Second call from same IP is rejected by pre-auth IP interceptor BEFORE resolve is invoked.
	ctx2 := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer tok_2")
	_, err = client.ListSlices(ctx2, &corev1.ListSlicesRequest{Account: "acme"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second ListSlices code = %v, want ResourceExhausted", status.Code(err))
	}
	if resolveCalls != 1 {
		t.Fatalf("resolveCalls after rate-limited RPC = %d, want 1 (pre-auth rejection)", resolveCalls)
	}
}

func TestTriggerRunFailureRateLimiter(t *testing.T) {
	// 1. Direct gRPC interceptor check: non-auth errors and successes do not trip the limiter,
	// while triggerAuthFailBurst auth failures exhaust the bucket.
	limiters := newServerLimiters(Config{
		RateLimitHTTPPerIPRPS:    1000,
		RateLimitHTTPPerIPBurst:  1000,
		RateLimitPerSubjectRPS:   1000,
		RateLimitPerSubjectBurst: 1000,
	})
	inter := limiters.triggerRunFailureUnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/gitslice.core.v1.TriggerRunService/StartRun"}
	peerCtx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.55"), Port: 9000},
	})

	for i := 0; i < 20; i++ {
		if _, err := inter(peerCtx, nil, info, func(context.Context, any) (any, error) {
			return "ok", nil
		}); err != nil {
			t.Fatalf("valid call #%d failed: %v", i, err)
		}
	}

	unauthHandler := func(context.Context, any) (any, error) {
		return nil, status.Error(codes.Unauthenticated, "invalid run token")
	}
	for i := 0; i < triggerAuthFailBurst; i++ {
		_, err := inter(peerCtx, nil, info, unauthHandler)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("auth failure #%d code = %v, want Unauthenticated", i, status.Code(err))
		}
	}
	// Next attempt from same IP is blocked before reaching the handler.
	handlerReached := false
	_, err := inter(peerCtx, nil, info, func(context.Context, any) (any, error) {
		handlerReached = true
		return "ok", nil
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("post-burst code = %v, want ResourceExhausted", status.Code(err))
	}
	if handlerReached {
		t.Fatal("handler should not be reached once triggerFail bucket is exhausted")
	}

	// 2. Connect HTTP path check: bad run token attempts trip ResourceExhausted after burst.
	mem := memory.New()
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
		Triggers:   mem.Triggers,
	}, mem.Objects, nil)
	handlers.SetTriggerEngine(triggers.NewEngine(triggers.Stores{
		Triggers:   mem.Triggers,
		Changesets: mem.Changesets,
		Slices:     mem.Slices,
		Repository: mem.Repository,
		Checks:     mem.Checks,
	}, triggers.Options{}))
	httpLimiters := newServerLimiters(Config{
		RateLimitHTTPPerIPRPS:    1000,
		RateLimitHTTPPerIPBurst:  1000,
		RateLimitPerSubjectRPS:   1000,
		RateLimitPerSubjectBurst: 1000,
	})
	api := newConnectHandler(func(context.Context, string) (string, error) {
		return "", nil
	}, handlers, httpLimiters)
	srv := httptest.NewServer(newHTTPHandler(api, nil, "", Config{}, false, httpLimiters))
	t.Cleanup(srv.Close)

	client := corev1connect.NewTriggerRunServiceClient(srv.Client(), srv.URL)
	for i := 0; i < triggerAuthFailBurst; i++ {
		_, err := client.GetRunState(context.Background(), connect.NewRequest(&corev1.TriggerRunTokenRequest{
			RunToken: "bad_token_guess",
		}))
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("Connect bad token #%d code = %v, want unauthenticated", i, connect.CodeOf(err))
		}
	}
	_, err = client.GetRunState(context.Background(), connect.NewRequest(&corev1.TriggerRunTokenRequest{
		RunToken: "bad_token_guess",
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("Connect post-burst code = %v, want resource_exhausted (err=%v)", connect.CodeOf(err), err)
	}
}

func TestHTTPRateLimitDisabledPassesThrough(t *testing.T) {
	handler := newHTTPRateLimitMiddleware(Config{
		RateLimitDisabled:       true,
		RateLimitHTTPPerIPRPS:   1,
		RateLimitHTTPPerIPBurst: 1,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < 3; i++ {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
		if res.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want %d", i, res.Code, http.StatusNoContent)
		}
	}
}

func TestMetricsTokenGate(t *testing.T) {
	handler := withMetricsToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "secret")

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want %d", res.Code, http.StatusUnauthorized)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("bearer token status = %d, want %d", res.Code, http.StatusNoContent)
	}

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/metrics?token=secret", nil))
	if res.Code != http.StatusNoContent {
		t.Fatalf("query token status = %d, want %d", res.Code, http.StatusNoContent)
	}
}

type testServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *testServerStream) Context() context.Context {
	return s.ctx
}

// The Git HTTP per-IP limit does not apply to gRPC: gs and CI daemons behind a
// shared Cloudflare edge address make many calls a second.
func TestGRPCServerIgnoresGitHTTPPerIPLimit(t *testing.T) {
	mem := memory.New()
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)
	resolve := func(ctx context.Context, token string) (string, error) { return "user_alice", nil }
	grpcSrv := NewGRPCServer(resolve, handlers, Config{
		RateLimitHTTPPerIPRPS:    0.001,
		RateLimitHTTPPerIPBurst:  1,
		RateLimitPerSubjectRPS:   100,
		RateLimitPerSubjectBurst: 100,
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := corev1.NewSliceServiceClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer gs_test")
	for i := 0; i < 10; i++ {
		if _, err := client.ListSlices(ctx, &corev1.ListSlicesRequest{}); status.Code(err) == codes.ResourceExhausted {
			t.Fatalf("call %d was rate limited by the Git HTTP per-IP limit", i+1)
		}
	}
}
