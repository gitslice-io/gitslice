package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestRegisterAgentIsPublic(t *testing.T) {
	if !isPublicMethod(agentSignupMethod) {
		t.Fatalf("%s must be callable without a bearer token", agentSignupMethod)
	}
}

func TestAgentSignupLimiterGRPC(t *testing.T) {
	interceptor := newRPCGuard(nil, newServerLimiters(Config{
		Auth: AuthConfig{AgentSignupPerHour: 2},
	})).grpcUnaryInterceptor()
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
	limiters := newServerLimiters(Config{
		Auth:      AuthConfig{AgentSignupPerHour: 2},
		RateLimit: RateLimitConfig{Disabled: true},
	})
	if limiters.signup != nil {
		t.Fatal("limiter must be nil when rate limiting is disabled")
	}
	interceptor := newRPCGuard(nil, limiters).grpcUnaryInterceptor()
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	for i := 0; i < 10; i++ {
		if _, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: agentSignupMethod}, handler); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentSignupLimiterConnectHTTP(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	limiters := newServerLimiters(Config{Auth: AuthConfig{AgentSignupPerHour: 1}})
	resolve := func(context.Context, string) (*authctx.Principal, error) {
		return &authctx.Principal{SubjectID: "user_1"}, nil
	}
	handler := newRPCGuard(resolve, limiters).connectMiddleware()(next)
	call := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Connect-Protocol-Version", "1")
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

func TestAgentSignupLimiterUsesForwardedClientIP(t *testing.T) {
	interceptor := newRPCGuard(nil, newServerLimiters(Config{
		Auth: AuthConfig{AgentSignupPerHour: 1},
	})).grpcUnaryInterceptor()
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: agentSignupMethod}
	// Every request arrives from the same proxy peer, as on Cloud Run.
	viaProxy := func(forwarded string) context.Context {
		ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("169.254.1.1"), Port: 1234}})
		return metadata.NewIncomingContext(ctx, metadata.Pairs("x-forwarded-for", forwarded))
	}

	if _, err := interceptor(viaProxy("203.0.113.10"), nil, info, handler); err != nil {
		t.Fatalf("first client: %v", err)
	}
	if _, err := interceptor(viaProxy("198.51.100.20"), nil, info, handler); err != nil {
		t.Fatalf("a second client behind the same proxy must have its own budget: %v", err)
	}
	// The rightmost hop is the one our edge appended; a spoofed left hop is ignored.
	if _, err := interceptor(viaProxy("1.2.3.4, 203.0.113.10"), nil, info, handler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("spoofed left hop code = %v; want ResourceExhausted", status.Code(err))
	}
}
