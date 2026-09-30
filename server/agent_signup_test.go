package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestRegisterAgentIsPublic(t *testing.T) {
	if !isPublicMethod(agentSignupMethod) {
		t.Fatalf("%s must be callable without a bearer token", agentSignupMethod)
	}
}

func TestAgentSignupLimiterGRPC(t *testing.T) {
	interceptor := agentSignupUnaryInterceptor(newAgentSignupLimiter(Config{AgentSignupPerHour: 2}))
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	signup := &grpc.UnaryServerInfo{FullMethod: agentSignupMethod}
	other := &grpc.UnaryServerInfo{FullMethod: "/gitslice.core.v1.AuthService/StartCliLogin"}
	fromIP := func(ip string) context.Context {
		return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 1234}})
	}

	a := fromIP("203.0.113.10")
	for i := 0; i < 2; i++ {
		if _, err := interceptor(a, nil, signup, handler); err != nil {
			t.Fatalf("signup %d failed: %v", i+1, err)
		}
	}
	if _, err := interceptor(a, nil, signup, handler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("third signup code = %v; want ResourceExhausted", status.Code(err))
	}
	if _, err := interceptor(a, nil, other, handler); err != nil {
		t.Fatalf("other methods must not be limited: %v", err)
	}
	if _, err := interceptor(fromIP("203.0.113.11"), nil, signup, handler); err != nil {
		t.Fatalf("a different IP must have its own budget: %v", err)
	}
}

func TestAgentSignupLimiterDisabled(t *testing.T) {
	if newAgentSignupLimiter(Config{AgentSignupPerHour: 2, RateLimitDisabled: true}) != nil {
		t.Fatal("limiter must be nil when rate limiting is disabled")
	}
	interceptor := agentSignupUnaryInterceptor(nil)
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	for i := 0; i < 10; i++ {
		if _, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: agentSignupMethod}, handler); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentSignupLimiterHTTP(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := newAgentSignupHTTPMiddleware(Config{AgentSignupPerHour: 1})(next)
	call := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = "198.51.100.7:4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call(agentSignupMethod); code != http.StatusOK {
		t.Fatalf("first signup = %d", code)
	}
	if code := call(agentSignupMethod); code != http.StatusTooManyRequests {
		t.Fatalf("second signup = %d; want 429", code)
	}
	if code := call("/gitslice.core.v1.AuthService/PollCliLogin"); code != http.StatusOK {
		t.Fatalf("other path = %d; want 200", code)
	}
}
