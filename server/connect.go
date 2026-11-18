package server

//go:generate go run ./internal/connectgen/cmd -out connect_adapters_gen.go

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/metrics"
	"gitslice.io/gitslice/internal/requestid"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc/status"
)

func NewConnectHandler(resolve subjectResolver, handlers *service.Handlers, cfgs ...Config) http.Handler {
	return NewPrincipalConnectHandler(toPrincipalResolver(resolve), handlers, cfgs...)
}

func NewPrincipalConnectHandler(resolve principalResolver, handlers *service.Handlers, cfgs ...Config) http.Handler {
	var limiters *serverLimiters
	if len(cfgs) > 0 {
		limiters = newServerLimiters(cfgs[0])
	}
	return newPrincipalConnectHandler(resolve, handlers, limiters)
}

func newConnectHandler(resolve subjectResolver, handlers *service.Handlers, limiters *serverLimiters) http.Handler {
	return newPrincipalConnectHandler(toPrincipalResolver(resolve), handlers, limiters)
}

func newPrincipalConnectHandler(resolve principalResolver, handlers *service.Handlers, limiters *serverLimiters) http.Handler {
	mux := http.NewServeMux()
	opts := []connect.HandlerOption{connect.WithInterceptors(connectMetricsInterceptor())}
	mount := func(pattern string, handler http.Handler) {
		mux.Handle(pattern, connectPrincipalAuthMiddlewareWithLimiters(resolve, limiters)(handler))
	}
	// Shared-link preview images; anonymous by design (see social_image.go).
	mux.Handle("/og/", limiters.httpIPMiddleware()(newSocialImageHandler(handlers.Slice)))
	// Account pictures; public, like the profile they belong to.
	mux.Handle("/avatars/", limiters.httpIPMiddleware()(newAvatarImageHandler(handlers.Auth)))
	mountConnectServices(mount, handlers, limiters, opts...)
	return mux
}

func connectAuthMiddleware(resolve subjectResolver) func(http.Handler) http.Handler {
	return connectPrincipalAuthMiddlewareWithLimiters(toPrincipalResolver(resolve), nil)
}

func connectPrincipalAuthMiddlewareWithLimiters(resolve principalResolver, limiters *serverLimiters) func(http.Handler) http.Handler {
	errorWriter := connect.NewErrorWriter()
	reject := func(w http.ResponseWriter, r *http.Request, start time.Time, err error) {
		recordGRPCRequest(r.Context(), r.URL.Path, connectStatusCode(err), time.Since(start), err)
		_ = errorWriter.Write(w, r, err)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := requestid.New()
			w.Header().Set("X-Request-Id", id)
			r = r.WithContext(requestid.With(r.Context(), id))

			var ipKey string
			if limiters != nil {
				ipKey = "ip:" + httpClientIPWithProxies(r, limiters.proxies)
			}
			if limiters != nil && limiters.grpcIP != nil && !limiters.grpcIP.Allow(ipKey) {
				ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
				reject(w, r, start, connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded")))
				return
			}

			if strings.HasPrefix(r.URL.Path, triggerRunServicePrefix) {
				if limiters != nil && limiters.triggerFail != nil {
					if !limiters.triggerFail.Peek(ipKey) {
						ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
						reject(w, r, start, connect.NewError(connect.CodeResourceExhausted, errors.New("trigger run authentication failure rate limit exceeded")))
						return
					}
					r = r.WithContext(withClientIPKey(r.Context(), ipKey))
				}
				next.ServeHTTP(w, r)
				return
			}

			if isPublicMethod(r.URL.Path) {
				if r.URL.Path == agentSignupMethod && limiters != nil && limiters.signup != nil && !limiters.signup.Allow(ipKey) {
					ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
					reject(w, r, start, connect.NewError(connect.CodeResourceExhausted, errors.New("agent sign-up rate limit exceeded")))
					return
				}
				if limiters != nil && limiters.subject != nil && limiters.grpcIP == nil && !limiters.subject.Allow(ipKey) {
					ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
					reject(w, r, start, connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded")))
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			token, ok, err := optionalBearerTokenValue(r.Header.Get("Authorization"))
			if err != nil {
				reject(w, r, start, connectError(err))
				return
			}
			if !ok {
				if limiters != nil && limiters.subject != nil && limiters.grpcIP == nil && !limiters.subject.Allow(ipKey) {
					ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
					reject(w, r, start, connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded")))
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			principal, err := resolve(r.Context(), token)
			if err != nil {
				reject(w, r, start, connectAuthError(err))
				return
			}
			if principal == nil {
				reject(w, r, start, connect.NewError(connect.CodeUnauthenticated, errors.New("missing authenticated principal")))
				return
			}
			if limiters != nil && limiters.subject != nil && !limiters.subject.Allow("subject:"+principal.SubjectID) {
				ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
				reject(w, r, start, connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded")))
				return
			}
			next.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), *principal)))
		})
	}
}

func connectResponse[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(msg), nil
}

func connectError(err error) error {
	if err == nil {
		return nil
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		return connect.NewError(connect.Code(st.Code()), errors.New(st.Message()))
	}
	return err
}

func connectAuthError(err error) error {
	return connectError(grpcAuthError(err))
}
