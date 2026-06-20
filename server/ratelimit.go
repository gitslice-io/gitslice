package server

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/metrics"
	"github.com/gitslice-io/gitslice/internal/ratelimit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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

func grpcRateLimitKey(ctx context.Context) string {
	if subjectID, ok := authctx.SubjectID(ctx); ok {
		return "subject:" + subjectID
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		if host := hostFromAddr(p.Addr.String()); host != "" {
			return "ip:" + host
		}
	}
	return "ip:unknown"
}

func isHealthCheckMethod(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

// httpClientIP uses X-Forwarded-For's first hop as a best-effort client key
// because staging is deployed behind a proxy. Treat this as trustworthy only
// when the edge proxy controls or strips incoming X-Forwarded-For values.
func httpClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if first = strings.TrimSpace(first); first != "" {
			return first
		}
	}
	if host := hostFromAddr(r.RemoteAddr); host != "" {
		return host
	}
	return "unknown"
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
