package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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

func TestHTTPRateLimitUsesForwardedForFirstHop(t *testing.T) {
	handler := newHTTPRateLimitMiddleware(Config{
		RateLimitHTTPPerIPRPS:   1,
		RateLimitHTTPPerIPBurst: 1,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.50:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.1")
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
	req.Header.Set("X-Forwarded-For", "203.0.113.11, 10.0.0.1")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("different forwarded client status = %d, want %d", res.Code, http.StatusNoContent)
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
