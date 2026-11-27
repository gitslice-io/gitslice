package server

import (
	"net/http"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/rpclimits"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
)

func NewCombinedGRPCGatewayHandler(grpcServer *grpc.Server, gateway http.Handler) http.Handler {
	return newCombinedServerHandler(grpcServer, gateway, nil)
}

func newCombinedServerHandler(grpcServer *grpc.Server, gateway http.Handler, gate *activity.Gate) http.Handler {
	boundedGateway := withNonStreamingDeadlines(gateway, gatewayReadTimeout, gatewayWriteTimeout)
	inner := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		boundedGateway.ServeHTTP(w, r)
	}))
	// Apply activity tracking inside h2c.NewHandler so each HTTP/2 stream has
	// its individual path/method inspected (rather than holding the connection's
	// PRI * HTTP/2.0 upgrade request in-flight for the lifetime of the socket).
	if gate != nil {
		inner = gate.Middleware(inner)
	}
	return h2c.NewHandler(inner, &http2.Server{
		// Receive windows for uploads (UploadBlobs, blob streams); the
		// defaults (1 MiB) cap a batch upload at a window per round trip.
		MaxUploadBufferPerStream:     4 << 20,
		MaxUploadBufferPerConnection: 16 << 20,
	})
}

func withNonStreamingDeadlines(next http.Handler, readTimeout, writeTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isStreamingHTTPRequest(r) {
			rc := http.NewResponseController(w)
			now := time.Now()
			_ = rc.SetReadDeadline(now.Add(readTimeout))
			_ = rc.SetWriteDeadline(now.Add(writeTimeout))
		}
		next.ServeHTTP(w, r)
	})
}

func isStreamingHTTPRequest(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/git/") {
		return true
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "application/grpc") || strings.HasPrefix(ct, "application/connect+") {
		return true
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		return true
	}
	return streamingProcedures[r.URL.Path]
}

func NewPrincipalGRPCServer(resolve principalResolver, handlers *service.Handlers, cfgs ...Config) *grpc.Server {
	var cfg Config
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	return newPrincipalGRPCServer(resolve, handlers, newServerLimiters(cfg))
}

func newPrincipalGRPCServer(resolve principalResolver, handlers *service.Handlers, limiters *serverLimiters) *grpc.Server {
	guard := newRPCGuard(resolve, limiters)
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.MaxSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.ChainUnaryInterceptor(
			requestIDUnaryInterceptor(),
			grpcMetricsUnaryInterceptor(),
			guard.grpcUnaryInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			requestIDStreamInterceptor(),
			grpcMetricsStreamInterceptor(),
			guard.grpcStreamInterceptor(),
		),
	)
	corev1.RegisterAuthServiceServer(grpcServer, handlers.Auth)
	corev1.RegisterRepositoryServiceServer(grpcServer, handlers.Repository)
	corev1.RegisterBlobServiceServer(grpcServer, handlers.Blob)
	corev1.RegisterSliceServiceServer(grpcServer, handlers.Slice)
	corev1.RegisterWorkspaceServiceServer(grpcServer, handlers.Workspace)
	corev1.RegisterChangesetServiceServer(grpcServer, handlers.Changeset)
	corev1.RegisterChangesetStackServiceServer(grpcServer, handlers.Stack)
	corev1.RegisterAgentServiceServer(grpcServer, handlers.Agent)
	corev1.RegisterCheckServiceServer(grpcServer, handlers.Check)
	corev1.RegisterSecretServiceServer(grpcServer, handlers.Secret)
	corev1.RegisterFeedbackServiceServer(grpcServer, handlers.Feedback)
	corev1.RegisterAutomationServiceServer(grpcServer, handlers.Automation)
	corev1.RegisterAutomationRunServiceServer(grpcServer, handlers.AutomationRun)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	return grpcServer
}
