package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/metrics"
	"gitslice.io/gitslice/internal/requestid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// agentSignupMethod and agentJoinInviteMethod are the unauthenticated agent
	// registration RPCs. Both get a tighter per-IP limit on top of the general
	// limiters because each call creates an account.
	agentSignupMethod     = "/gitslice.core.v1.AuthService/RegisterAgent"
	agentJoinInviteMethod = "/gitslice.core.v1.AuthService/JoinAgentInvite"

	// automationRunServicePrefix identifies unauthenticated-at-interceptor RPCs
	// that carry a run token in the request body; failed token guesses get a
	// dedicated strict per-IP budget.
	automationRunServicePrefix = "/gitslice.core.v1.AutomationRunService/"
)

// rpcGuard owns all transport-agnostic RPC admission policy shared by gRPC
// interceptors and Connect HTTP middleware: public method allowlist, pre-auth
// IP rate limiting, agent sign-up limiting, trigger-run auth-failure limiting,
// bearer-token authentication, and per-subject rate limiting.
type rpcGuard struct {
	resolve  principalResolver
	limiters *serverLimiters
}

func newRPCGuard(resolve principalResolver, limiters *serverLimiters) *rpcGuard {
	return &rpcGuard{
		resolve:  resolve,
		limiters: limiters,
	}
}

func isPublicMethod(method string) bool {
	return method == "/gitslice.core.v1.AuthService/StartCliLogin" ||
		method == "/gitslice.core.v1.AuthService/PollCliLogin" ||
		method == agentSignupMethod ||
		method == agentJoinInviteMethod ||
		// A trigger run authenticates with its run token, in the request
		// (design/34_triggers.md).
		isAutomationRunMethod(method) ||
		isHealthCheckMethod(method)
}

func isHealthCheckMethod(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

func isAgentSignupMethod(method string) bool {
	return method == agentSignupMethod || method == agentJoinInviteMethod
}

func isAutomationRunMethod(method string) bool {
	return strings.HasPrefix(method, automationRunServicePrefix)
}

func isAuthFailureCode(code codes.Code) bool {
	return code == codes.Unauthenticated || code == codes.PermissionDenied
}

// admit enforces all pre-handler RPC policy for a single unary or streaming
// invocation, returning gRPC status errors that Connect maps via connectError.
func (g *rpcGuard) admit(ctx context.Context, transport, method, ipKey, authHeader string, isUnary bool) (context.Context, error) {
	if isHealthCheckMethod(method) {
		return ctx, nil
	}
	if err := g.checkPreAuthIP(transport, ipKey); err != nil {
		return nil, err
	}
	if isAutomationRunMethod(method) {
		if isUnary {
			if err := g.peekTriggerRunFailure(transport, ipKey); err != nil {
				return nil, err
			}
			ctx = withClientIPKey(ctx, ipKey)
		}
		if err := g.checkAnonymousSubjectFallback(transport, ipKey); err != nil {
			return nil, err
		}
		return ctx, nil
	}
	if isPublicMethod(method) {
		if isUnary {
			if err := g.checkAgentSignup(transport, method, ipKey); err != nil {
				return nil, err
			}
		}
		if err := g.checkAnonymousSubjectFallback(transport, ipKey); err != nil {
			return nil, err
		}
		return ctx, nil
	}
	ctx, err := g.authenticate(ctx, authHeader)
	if err != nil {
		return nil, err
	}
	if err := g.checkSubject(ctx, transport, ipKey); err != nil {
		return nil, err
	}
	return ctx, nil
}

func (g *rpcGuard) checkPreAuthIP(transport, ipKey string) error {
	if g.limiters == nil || g.limiters.grpcIP == nil {
		return nil
	}
	if g.limiters.grpcIP.Allow(ipKey) {
		return nil
	}
	ratelimitRejectedTotal.Inc(metrics.Labels{"transport": transport})
	return status.Error(codes.ResourceExhausted, "rate limit exceeded")
}

func (g *rpcGuard) checkAgentSignup(transport, method, ipKey string) error {
	if !isAgentSignupMethod(method) || g.limiters == nil || g.limiters.signup == nil {
		return nil
	}
	if g.limiters.signup.Allow(ipKey) {
		return nil
	}
	ratelimitRejectedTotal.Inc(metrics.Labels{"transport": transport})
	return status.Error(codes.ResourceExhausted, "agent sign-up rate limit exceeded")
}

func (g *rpcGuard) peekTriggerRunFailure(transport, ipKey string) error {
	if g.limiters == nil || g.limiters.triggerFail == nil {
		return nil
	}
	if g.limiters.triggerFail.Peek(ipKey) {
		return nil
	}
	ratelimitRejectedTotal.Inc(metrics.Labels{"transport": transport})
	return status.Error(codes.ResourceExhausted, "trigger run authentication failure rate limit exceeded")
}

// recordTriggerRunFailure charges the trigger-run auth-failure budget when an
// AutomationRunService call fails authentication/authorization.
func (g *rpcGuard) recordTriggerRunFailure(transport, ipKey string, code codes.Code) error {
	if g.limiters == nil || g.limiters.triggerFail == nil || ipKey == "" || !isAuthFailureCode(code) {
		return nil
	}
	if g.limiters.triggerFail.Allow(ipKey) {
		return nil
	}
	ratelimitRejectedTotal.Inc(metrics.Labels{"transport": transport})
	return status.Error(codes.ResourceExhausted, "trigger run authentication failure rate limit exceeded")
}

func (g *rpcGuard) authenticate(ctx context.Context, authHeader string) (context.Context, error) {
	token, ok, err := optionalBearerTokenValue(authHeader)
	if err != nil {
		return nil, err
	}
	if !ok {
		return ctx, nil
	}
	if g.resolve == nil {
		return nil, status.Error(codes.Unauthenticated, "missing principal resolver")
	}
	principal, err := g.resolve(ctx, token)
	if err != nil {
		return nil, grpcAuthError(err)
	}
	if principal == nil {
		return nil, status.Error(codes.Unauthenticated, "missing authenticated principal")
	}
	return authctx.WithPrincipal(ctx, *principal), nil
}

func (g *rpcGuard) checkSubject(ctx context.Context, transport, ipKey string) error {
	if g.limiters == nil || g.limiters.subject == nil {
		return nil
	}
	if subjectID, ok := authctx.SubjectID(ctx); ok {
		if g.limiters.subject.Allow("subject:" + subjectID) {
			return nil
		}
		ratelimitRejectedTotal.Inc(metrics.Labels{"transport": transport})
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	return g.checkAnonymousSubjectFallback(transport, ipKey)
}

func (g *rpcGuard) checkAnonymousSubjectFallback(transport, ipKey string) error {
	// When the pre-auth per-IP limiter is enabled, anonymous callers were
	// already charged against grpcIP before auth; otherwise fall back to peer IP.
	if g.limiters == nil || g.limiters.subject == nil || g.limiters.grpcIP != nil {
		return nil
	}
	if g.limiters.subject.Allow(ipKey) {
		return nil
	}
	ratelimitRejectedTotal.Inc(metrics.Labels{"transport": transport})
	return status.Error(codes.ResourceExhausted, "rate limit exceeded")
}

func (g *rpcGuard) proxies() trustedProxies {
	if g.limiters == nil {
		return trustedProxies{}
	}
	return g.limiters.proxies
}

func (g *rpcGuard) grpcUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ipKey := grpcPeerIPKeyWithProxies(ctx, g.proxies())
		ctx, err := g.admit(ctx, "grpc", info.FullMethod, ipKey, grpcAuthHeader(ctx), true)
		if err != nil {
			return nil, err
		}
		resp, err := handler(ctx, req)
		if isAutomationRunMethod(info.FullMethod) {
			if limitErr := g.recordTriggerRunFailure("grpc", ipKey, status.Code(err)); limitErr != nil {
				return nil, limitErr
			}
		}
		return resp, err
	}
}

func (g *rpcGuard) grpcStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := stream.Context()
		ipKey := grpcPeerIPKeyWithProxies(ctx, g.proxies())
		ctx, err := g.admit(ctx, "grpc", info.FullMethod, ipKey, grpcAuthHeader(ctx), false)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{
			ServerStream: stream,
			ctx:          ctx,
		})
	}
}

func (g *rpcGuard) connectMiddleware() func(http.Handler) http.Handler {
	errorWriter := connect.NewErrorWriter()
	reject := func(w http.ResponseWriter, r *http.Request, start time.Time, err error) {
		connectErr := connectError(err)
		recordGRPCRequest(r.Context(), r.URL.Path, connectStatusCode(connectErr), time.Since(start), connectErr)
		_ = errorWriter.Write(w, r, connectErr)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := requestid.New()
			w.Header().Set("X-Request-Id", id)
			r = r.WithContext(requestid.With(r.Context(), id))

			ipKey := "ip:" + httpClientIPWithProxies(r, g.proxies())
			ctx, err := g.admit(r.Context(), "http", r.URL.Path, ipKey, r.Header.Get("Authorization"), true)
			if err != nil {
				reject(w, r, start, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func grpcAuthHeader(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
