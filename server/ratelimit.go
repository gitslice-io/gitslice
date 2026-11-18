package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
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

const (
	rateLimitBucketTTL = 10 * time.Minute

	// agentSignupMethod is the unauthenticated agent self-registration RPC. It
	// gets its own much tighter per-IP limit on top of the general limiters,
	// because each call creates an account. Connect serves it on the same path.
	agentSignupMethod = "/gitslice.core.v1.AuthService/RegisterAgent"

	// agentSignupBucketTTL outlives the refill period so an idle IP's spent
	// bucket is not forgotten before it would have refilled anyway.
	agentSignupBucketTTL = 2 * time.Hour

	// triggerRunServicePrefix identifies unauthenticated-at-interceptor RPCs
	// that carry a run token in the request body; failed token guesses get a
	// dedicated strict per-IP budget.
	triggerRunServicePrefix = "/gitslice.core.v1.TriggerRunService/"
	triggerAuthFailRPS      = 0.2
	triggerAuthFailBurst    = 10
	triggerAuthFailTTL      = 15 * time.Minute
)

var ratelimitRejectedTotal = metrics.NewCounter(
	"gitslice_ratelimit_rejected_total",
	"Requests rejected by in-process rate limiting.",
	"transport",
)

type trustedProxies struct {
	explicit bool
	prefixes []netip.Prefix
}

func parseTrustedProxies(entries []string) (trustedProxies, error) {
	if len(entries) == 0 {
		return trustedProxies{}, nil
	}
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return trustedProxies{}, fmt.Errorf("invalid GITSLICE_TRUSTED_PROXIES CIDR %q: %w", entry, err)
			}
			addr := prefix.Addr().Unmap()
			bits := prefix.Bits()
			if prefix.Addr().Is6() && addr.Is4() {
				bits -= 96
				if bits < 0 {
					bits = 0
				}
			}
			prefixes = append(prefixes, netip.PrefixFrom(addr, bits).Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return trustedProxies{}, fmt.Errorf("invalid GITSLICE_TRUSTED_PROXIES IP %q: %w", entry, err)
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	if len(prefixes) == 0 {
		return trustedProxies{}, nil
	}
	return trustedProxies{explicit: true, prefixes: prefixes}, nil
}

func (t trustedProxies) isTrusted(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	if t.explicit {
		for _, p := range t.prefixes {
			if p.Contains(addr) {
				return true
			}
		}
		return false
	}
	// Default: loopback, RFC1918/ULA private reverse proxies, and Cloud Run's
	// link-local Google Front End proxy (169.254.x.x / fe80::/10).
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// serverLimiters holds the rate limiters shared across HTTP, Connect, Git, and
// gRPC transports so a single client cannot multiply its quota across ports or
// protocols.
type serverLimiters struct {
	proxies trustedProxies
	// ip limits Git HTTP requests per client IP (GITSLICE_RATELIMIT_HTTP_*).
	ip *ratelimit.Limiter
	// grpcIP limits gRPC calls per client IP before authentication, so a flood
	// can't reach the subject resolver unchecked. It uses the per-subject rate:
	// behind Cloudflare the client IP is often a shared edge address, and gs
	// and CI daemons make many calls (lazy mounts, checkouts), which the much
	// lower Git HTTP limit would throttle for everyone behind that edge.
	grpcIP      *ratelimit.Limiter
	subject     *ratelimit.Limiter
	signup      *ratelimit.Limiter
	triggerFail *ratelimit.Limiter
}

func newServerLimiters(cfg Config) *serverLimiters {
	proxies, err := parseTrustedProxies(cfg.TrustedProxyCIDRs)
	if err != nil {
		slog.Warn("ignoring invalid trusted proxy CIDRs in rate limiter; validate config before starting server", "error", err)
	}
	if cfg.RateLimitDisabled {
		return &serverLimiters{proxies: proxies}
	}
	var ipLimiter *ratelimit.Limiter
	if cfg.RateLimitHTTPPerIPRPS > 0 {
		ipLimiter = ratelimit.New(cfg.RateLimitHTTPPerIPRPS, serverRateLimitBurst(cfg.RateLimitHTTPPerIPBurst), rateLimitBucketTTL)
	}
	var grpcIPLimiter *ratelimit.Limiter
	if cfg.RateLimitPerSubjectRPS > 0 {
		grpcIPLimiter = ratelimit.New(cfg.RateLimitPerSubjectRPS, serverRateLimitBurst(cfg.RateLimitPerSubjectBurst), rateLimitBucketTTL)
	}
	return &serverLimiters{
		proxies:     proxies,
		ip:          ipLimiter,
		grpcIP:      grpcIPLimiter,
		subject:     newGRPCRateLimiter(cfg),
		signup:      newAgentSignupLimiter(cfg),
		triggerFail: ratelimit.New(triggerAuthFailRPS, triggerAuthFailBurst, triggerAuthFailTTL),
	}
}

func (l *serverLimiters) httpIPMiddleware() func(http.Handler) http.Handler {
	if l == nil || l.ip == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				next.ServeHTTP(w, r)
				return
			}
			if !l.ip.Allow("ip:" + httpClientIPWithProxies(r, l.proxies)) {
				ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (l *serverLimiters) grpcPreAuthUnaryInterceptor() grpc.UnaryServerInterceptor {
	if l == nil || l.grpcIP == nil {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isHealthCheckMethod(info.FullMethod) || l.grpcIP.Allow(grpcPeerIPKeyWithProxies(ctx, l.proxies)) {
			return handler(ctx, req)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

func (l *serverLimiters) grpcPreAuthStreamInterceptor() grpc.StreamServerInterceptor {
	if l == nil || l.grpcIP == nil {
		return func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, stream)
		}
	}
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isHealthCheckMethod(info.FullMethod) || l.grpcIP.Allow(grpcPeerIPKeyWithProxies(stream.Context(), l.proxies)) {
			return handler(srv, stream)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

func (l *serverLimiters) grpcSubjectUnaryInterceptor() grpc.UnaryServerInterceptor {
	if l == nil || l.subject == nil {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isHealthCheckMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		key, check := l.subjectOrFallbackKey(ctx)
		if !check || l.subject.Allow(key) {
			return handler(ctx, req)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

func (l *serverLimiters) grpcSubjectStreamInterceptor() grpc.StreamServerInterceptor {
	if l == nil || l.subject == nil {
		return func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, stream)
		}
	}
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isHealthCheckMethod(info.FullMethod) {
			return handler(srv, stream)
		}
		key, check := l.subjectOrFallbackKey(stream.Context())
		if !check || l.subject.Allow(key) {
			return handler(srv, stream)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
}

func (l *serverLimiters) subjectOrFallbackKey(ctx context.Context) (string, bool) {
	if subjectID, ok := authctx.SubjectID(ctx); ok {
		return "subject:" + subjectID, true
	}
	// When the pre-auth per-IP limiter is enabled, anonymous callers were
	// already charged against l.grpcIP before auth; otherwise fall back to peer IP.
	if l.grpcIP != nil {
		return "", false
	}
	return grpcPeerIPKeyWithProxies(ctx, l.proxies), true
}

func (l *serverLimiters) agentSignupUnaryInterceptor() grpc.UnaryServerInterceptor {
	if l == nil || l.signup == nil {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod != agentSignupMethod || l.signup.Allow(grpcPeerIPKeyWithProxies(ctx, l.proxies)) {
			return handler(ctx, req)
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
		return nil, status.Error(codes.ResourceExhausted, "agent sign-up rate limit exceeded")
	}
}

func (l *serverLimiters) triggerRunFailureUnaryInterceptor() grpc.UnaryServerInterceptor {
	if l == nil || l.triggerFail == nil {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !strings.HasPrefix(info.FullMethod, triggerRunServicePrefix) {
			return handler(ctx, req)
		}
		ipKey := grpcPeerIPKeyWithProxies(ctx, l.proxies)
		if !l.triggerFail.Peek(ipKey) {
			ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
			return nil, status.Error(codes.ResourceExhausted, "trigger run authentication failure rate limit exceeded")
		}
		resp, err := handler(ctx, req)
		if isAuthFailureError(err) && !l.triggerFail.Allow(ipKey) {
			ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "grpc"})
			return nil, status.Error(codes.ResourceExhausted, "trigger run authentication failure rate limit exceeded")
		}
		return resp, err
	}
}

func isAuthFailureError(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return true
	default:
		return false
	}
}

type clientIPContextKey struct{}

func withClientIPKey(ctx context.Context, ipKey string) context.Context {
	return context.WithValue(ctx, clientIPContextKey{}, ipKey)
}

func clientIPKeyFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(clientIPContextKey{}).(string)
	return v, ok && v != ""
}

func newGRPCRateLimiter(cfg Config) *ratelimit.Limiter {
	if cfg.RateLimitDisabled || cfg.RateLimitPerSubjectRPS <= 0 {
		return nil
	}
	return ratelimit.New(cfg.RateLimitPerSubjectRPS, serverRateLimitBurst(cfg.RateLimitPerSubjectBurst), rateLimitBucketTTL)
}

func newAgentSignupLimiter(cfg Config) *ratelimit.Limiter {
	if cfg.RateLimitDisabled || cfg.AgentSignupPerHour <= 0 {
		return nil
	}
	return ratelimit.New(float64(cfg.AgentSignupPerHour)/3600, cfg.AgentSignupPerHour, agentSignupBucketTTL)
}

func grpcPeerIPKey(ctx context.Context) string {
	return grpcPeerIPKeyWithProxies(ctx, trustedProxies{})
}

func grpcPeerIPKeyWithProxies(ctx context.Context, proxies trustedProxies) string {
	var peerHost string
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		peerHost = hostFromAddr(p.Addr.String())
	}
	peerAddr, peerValid := parseNetIP(peerHost)
	if peerValid && proxies.isTrusted(peerAddr) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if fwdAddr, ok := rightmostForwardedIP(strings.Join(md.Get("x-forwarded-for"), ",")); ok {
				return "ip:" + rateLimitIPBucket(fwdAddr)
			}
		}
	}
	if peerValid {
		return "ip:" + rateLimitIPBucket(peerAddr)
	}
	if peerHost != "" {
		return "ip:" + peerHost
	}
	return "ip:unknown"
}

func isHealthCheckMethod(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

func httpClientIP(r *http.Request) string {
	return httpClientIPWithProxies(r, trustedProxies{})
}

func httpClientIPWithProxies(r *http.Request, proxies trustedProxies) string {
	peerHost := hostFromAddr(r.RemoteAddr)
	peerAddr, peerValid := parseNetIP(peerHost)
	if peerValid && proxies.isTrusted(peerAddr) {
		if fwdAddr, ok := rightmostForwardedIP(r.Header.Get("X-Forwarded-For")); ok {
			return rateLimitIPBucket(fwdAddr)
		}
	}
	if peerValid {
		return rateLimitIPBucket(peerAddr)
	}
	if peerHost != "" {
		return peerHost
	}
	return "unknown"
}

// rateLimitIPBucket normalizes IPv4 to /32 and IPv6 to its /64 subnet prefix
// so a client with a standard /64 allocation shares one rate-limit bucket.
func rateLimitIPBucket(addr netip.Addr) string {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return "unknown"
	}
	if addr.Is6() {
		prefix, err := addr.Prefix(64)
		if err == nil {
			return prefix.Masked().String()
		}
	}
	return addr.String()
}

func parseNetIP(host string) (netip.Addr, bool) {
	host = strings.TrimSpace(host)
	if host == "" {
		return netip.Addr{}, false
	}
	if idx := strings.IndexByte(host, '%'); idx >= 0 {
		host = host[:idx]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func rightmostForwardedIP(forwarded string) (netip.Addr, bool) {
	hops := strings.Split(forwarded, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if hop == "" {
			continue
		}
		if addr, ok := parseNetIP(hop); ok {
			return addr, true
		}
	}
	return netip.Addr{}, false
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
