package server

//go:generate go run ./internal/connectgen/cmd -out connect_adapters_gen.go

import (
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/avatar"
	"gitslice.io/gitslice/internal/mcpserver"
	"gitslice.io/gitslice/internal/socialcard"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc/status"
)

func NewPrincipalConnectHandler(resolve principalResolver, handlers *service.Handlers, cfgs ...Config) http.Handler {
	var cfg Config
	var limiters *serverLimiters
	if len(cfgs) > 0 {
		cfg = cfgs[0]
		limiters = newServerLimiters(cfg)
	}
	return newPrincipalConnectHandler(resolve, handlers, limiters, cfg.webBaseURL())
}

func newPrincipalConnectHandler(resolve principalResolver, handlers *service.Handlers, limiters *serverLimiters, webBaseURL string) http.Handler {
	mux := http.NewServeMux()
	guard := newRPCGuard(resolve, limiters)
	opts := []connect.HandlerOption{connect.WithInterceptors(connectMetricsInterceptor())}
	mount := func(pattern string, handler http.Handler) {
		mux.Handle(pattern, guard.connectMiddleware()(handler))
	}
	// Shared-link preview images; anonymous by design (internal/socialcard).
	var slices socialcard.SliceResolver
	var avatars avatar.Source
	if handlers != nil {
		slices = handlers.Slice
		avatars = handlers.Auth
	}
	mux.Handle("/og/", limiters.httpIPMiddleware()(socialcard.NewHandler(slices)))
	// Account pictures; public, like the profile they belong to (internal/avatar).
	mux.Handle("/avatars/", limiters.httpIPMiddleware()(avatar.NewHandler(avatars)))
	// Bash automation checkout archive; authenticated by per-run bearer token.
	mux.Handle("/automation-archive", limiters.httpIPMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handlers == nil || handlers.AutomationRun == nil {
			http.Error(w, "automations are not enabled on this server", http.StatusServiceUnavailable)
			return
		}
		handlers.AutomationRun.ServeArchiveHTTP(w, r)
	})))
	// Hosted Streamable HTTP MCP endpoint (design/40_agent_onboarding.md).
	mount("/mcp", mcpserver.NewHostedHTTPHandler(hostedMCPDeps(handlers, webBaseURL)))
	mountConnectServices(mount, handlers, limiters, opts...)
	return mux
}

func hostedMCPDeps(handlers *service.Handlers, webBaseURL string) mcpserver.HostedDeps {
	deps := mcpserver.HostedDeps{WebBaseURL: webBaseURL}
	if handlers == nil {
		return deps
	}
	deps.Slices = handlers.Slice
	deps.Repo = handlers.Repository
	deps.Files = handlers.Repository
	deps.Changesets = handlers.Changeset
	deps.Checks = handlers.Check
	deps.Blobs = handlers.Blob
	return deps
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
