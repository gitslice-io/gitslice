package server

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func NewHTTPGateway(ctx context.Context, grpcEndpoint string) (http.Handler, error) {
	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(gatewayHeaderMatcher))
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	registerHandlers := []func(context.Context, *runtime.ServeMux, string, []grpc.DialOption) error{
		corev1.RegisterFakeAccountServiceHandlerFromEndpoint,
		corev1.RegisterRepositoryServiceHandlerFromEndpoint,
		corev1.RegisterBlobServiceHandlerFromEndpoint,
		corev1.RegisterSliceServiceHandlerFromEndpoint,
		corev1.RegisterWorkspaceServiceHandlerFromEndpoint,
		corev1.RegisterChangesetServiceHandlerFromEndpoint,
	}
	for _, register := range registerHandlers {
		if err := register(ctx, mux, grpcEndpoint, opts); err != nil {
			return nil, err
		}
	}
	return mux, nil
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
