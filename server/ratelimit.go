package server

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/metrics"
	"gitslice.io/gitslice/internal/ratelimit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const rateLimitBucketTTL = 10 * time.Minute

var ratelimitRejectedTotal = metrics.NewCounter(
	"gitslice_ratelimit_rejected_total",
	"Requests rejected by in-process rate limiting.",
	"transport",
)

func newGRPCRateLimiter(cfg Config) *ratelimit.Limiter {
	if cfg.RateLimitDisabled || cfg.RateLimitPerSubjectRPS <= 0 {
		return nil
	}
	return ratelimit.New(cfg.RateLimitPerSubjectRPS, serverRateLimitBurst(cfg.RateLimitPerSubjectBurst), rateLimitBucketTTL)
}

func grpcRateLimitUnaryInterceptor(limiter *ratelimit.Limiter) grpc.UnaryServerInterceptor {
	if limiter == nil {
		return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isHealthCheckMethod(info.FullMethod) || limiter.Allow(grpcRateLimitKey(ctx)) {
			return handler(ctx, req)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

func grpcRateLimitStreamInterceptor(limiter *ratelimit.Limiter) grpc.StreamServerInterceptor {
	if limiter == nil {
		return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, stream)
		}
	}
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isHealthCheckMethod(info.FullMethod) || limiter.Allow(grpcRateLimitKey(stream.Context())) {
			return handler(srv, stream)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

func newHTTPRateLimitMiddleware(cfg Config) func(http.Handler) http.Handler {
	if cfg.RateLimitDisabled || cfg.RateLimitHTTPPerIPRPS <= 0 {
		return func(next http.Handler) http.Handler {
			return next
		}
	}
	limiter := ratelimit.New(cfg.RateLimitHTTPPerIPRPS, serverRateLimitBurst(cfg.RateLimitHTTPPerIPBurst), rateLimitBucketTTL)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow("ip:" + httpClientIP(r)) {
				ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// agentSignupMethod is the unauthenticated agent self-registration RPC. It gets
// its own much tighter per-IP limit on top of the general limiters, because each
// call creates an account. Connect serves it on the same path.
const agentSignupMethod = "/gitslice.core.v1.AuthService/RegisterAgent"

// agentSignupBucketTTL outlives the refill period so an idle IP's spent bucket
// is not forgotten (and silently refilled) before it would have refilled anyway.
const agentSignupBucketTTL = 2 * time.Hour

func newAgentSignupLimiter(cfg Config) *ratelimit.Limiter {
	if cfg.RateLimitDisabled || cfg.AgentSignupPerHour <= 0 {
		return nil
	}
	return ratelimit.New(float64(cfg.AgentSignupPerHour)/3600, cfg.AgentSignupPerHour, agentSignupBucketTTL)
}

func agentSignupUnaryInterceptor(limiter *ratelimit.Limiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if limiter == nil || info.FullMethod != agentSignupMethod || limiter.Allow(grpcPeerIPKey(ctx)) {
			return handler(ctx, req)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return nil, status.Error(codes.ResourceExhausted, "agent sign-up rate limit exceeded")
	}
}

func newAgentSignupHTTPMiddleware(cfg Config) func(http.Handler) http.Handler {
	limiter := newAgentSignupLimiter(cfg)
	if limiter == nil {
		return func(next http.Handler) http.Handler {
			return next
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == agentSignupMethod && !limiter.Allow("ip:"+httpClientIP(r)) {
				ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
				http.Error(w, "agent sign-up rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// grpcPeerIPKey keys an unauthenticated gRPC call by client IP. Behind Cloud
// Run (and nginx on staging) the TCP peer is the proxy, so every client would
// share one bucket; like httpClientIP it prefers the rightmost
// x-forwarded-for hop, which our own edge appends and a client cannot forge.
func grpcPeerIPKey(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if hop := rightmostForwardedHop(strings.Join(md.Get("x-forwarded-for"), ",")); hop != "" {
			return "ip:" + hop
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		if host := hostFromAddr(p.Addr.String()); host != "" {
			return "ip:" + host
		}
	}
	return "ip:unknown"
}

func grpcRateLimitKey(ctx context.Context) string {
	if subjectID, ok := authctx.SubjectID(ctx); ok {
		return "subject:" + subjectID
	}
	return grpcPeerIPKey(ctx)
}

func isHealthCheckMethod(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

// httpClientIP uses X-Forwarded-For's rightmost non-empty hop as the client key.
// That hop is appended by our own edge (Google's front end in production and
// nginx on staging), so it is the only entry a client cannot forge. Anything to
// its left is attacker-controlled.
func httpClientIP(r *http.Request) string {
	if hop := rightmostForwardedHop(r.Header.Get("X-Forwarded-For")); hop != "" {
		return hop
	}
	if host := hostFromAddr(r.RemoteAddr); host != "" {
		return host
	}
	return "unknown"
}

func rightmostForwardedHop(forwarded string) string {
	hops := strings.Split(forwarded, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		if hop := strings.TrimSpace(hops[i]); hop != "" {
			return hop
		}
	}
	return ""
}

func hostFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return strings.TrimSpace(host)
	}
	return strings.TrimSpace(addr)
}

func serverRateLimitBurst(burst int) int {
	if burst <= 0 {
		return 1
	}
	return burst
}
