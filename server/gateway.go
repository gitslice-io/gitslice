package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gitslice-io/gitslice/internal/metrics"
	"github.com/gitslice-io/gitslice/internal/rpclimits"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var openMetricsWarningOnce sync.Once

func NewHTTPGateway(ctx context.Context, grpcEndpoint string) (http.Handler, error) {
	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(gatewayHeaderMatcher))
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
			grpc.MaxCallSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		),
	}
	registerHandlers := []func(context.Context, *runtime.ServeMux, string, []grpc.DialOption) error{
		corev1.RegisterAuthServiceHandlerFromEndpoint,
		corev1.RegisterRepositoryServiceHandlerFromEndpoint,
		corev1.RegisterBlobServiceHandlerFromEndpoint,
		corev1.RegisterSliceServiceHandlerFromEndpoint,
		corev1.RegisterWorkspaceServiceHandlerFromEndpoint,
		corev1.RegisterChangesetServiceHandlerFromEndpoint,
		corev1.RegisterChangesetStackServiceHandlerFromEndpoint,
	}
	for _, register := range registerHandlers {
		if err := register(ctx, mux, grpcEndpoint, opts); err != nil {
			return nil, err
		}
	}
	return mux, nil
}

func NewHTTPHandler(gateway http.Handler, allowedOrigin string, cfgs ...Config) http.Handler {
	var cfg Config
	configured := false
	if len(cfgs) > 0 {
		cfg = cfgs[0]
		configured = true
	}

	mux := http.NewServeMux()
	metricsHandler := metrics.Handler()
	if token := strings.TrimSpace(cfg.MetricsToken); token != "" {
		metricsHandler = withMetricsToken(metricsHandler, token)
	} else if configured {
		warnOpenMetricsEndpoint()
	}
	mux.Handle("/metrics", metricsHandler)

	gatewayHandler := withMaxBody(gateway, rpclimits.MaxUnaryMessageBytes)
	gatewayHandler = newHTTPRateLimitMiddleware(cfg)(gatewayHandler)
	mux.Handle("/", withCORS(gatewayHandler, allowedOrigin))
	return mux
}

func withMetricsToken(handler http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validMetricsToken(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func validMetricsToken(r *http.Request, token string) bool {
	candidate := r.URL.Query().Get("token")
	if candidate == "" {
		const prefix = "bearer "
		value := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			candidate = strings.TrimSpace(value[len(prefix):])
		}
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1
}

func warnOpenMetricsEndpoint() {
	openMetricsWarningOnce.Do(func() {
		slog.Warn("gitslice /metrics endpoint is unauthenticated; set GITSLICE_METRICS_TOKEN to require access")
	})
}

// withMaxBody caps the request body so the JSON gateway cannot be forced to
// buffer an unbounded payload while translating to gRPC. The cap matches the
// unary gRPC message limit the gateway forwards to.
func withMaxBody(handler http.Handler, max int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		handler.ServeHTTP(w, r)
	})
}

func gatewayGRPCEndpoint(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func gatewayHeaderMatcher(key string) (string, bool) {
	if strings.EqualFold(key, "Authorization") {
		return "authorization", true
	}
	return runtime.DefaultHeaderMatcher(key)
}

func withCORS(handler http.Handler, allowedOrigin string) http.Handler {
	if strings.TrimSpace(allowedOrigin) == "" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := corsOrigin(allowedOrigin, r.Header.Get("Origin")); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func corsOrigin(allowedOrigin, requestOrigin string) string {
	for _, origin := range strings.Split(allowedOrigin, ",") {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			return "*"
		}
		if origin != "" && requestOrigin == origin {
			return origin
		}
	}
	return ""
}
