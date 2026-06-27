package server

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/gitslice-io/gitslice/internal/metrics"
	"github.com/gitslice-io/gitslice/internal/rpclimits"
)

var openMetricsWarningOnce sync.Once

func NewHTTPHandler(api http.Handler, allowedOrigin string, cfgs ...Config) http.Handler {
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

	apiHandler := withMaxBody(api, rpclimits.MaxUnaryMessageBytes)
	apiHandler = newHTTPRateLimitMiddleware(cfg)(apiHandler)
	mux.Handle("/", withCORS(apiHandler, allowedOrigin))
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

// withMaxBody caps the HTTP API request body so the server cannot be forced to
// buffer an unbounded unary payload.
func withMaxBody(handler http.Handler, max int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		handler.ServeHTTP(w, r)
	})
}

func withCORS(handler http.Handler, allowedOrigin string) http.Handler {
	if strings.TrimSpace(allowedOrigin) == "" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := corsOrigin(allowedOrigin, r.Header.Get("Origin")); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Connect-Protocol-Version, Connect-Timeout, Content-Type, X-User-Agent")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "Connect-Content-Encoding, Connect-Accept-Encoding, Grpc-Message, Grpc-Status, Grpc-Status-Details-Bin")
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
