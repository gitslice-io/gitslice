package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/proto/core/v1/corev1connect"
	"gitslice.io/gitslice/service"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAuthInterceptorAllowsMissingBearerForServiceLevelAuth(t *testing.T) {
	interceptor := authInterceptor(func(ctx context.Context, token string) (string, error) {
		t.Fatalf("resolver called for missing bearer token %q", token)
		return "", nil
	})
	called := false
	_, err := interceptor(context.Background(), "request", &grpc.UnaryServerInfo{FullMethod: "/gitslice.core.v1.SliceService/GetSlice"}, func(ctx context.Context, req any) (any, error) {
		called = true
		if _, ok := authctx.SubjectID(ctx); ok {
			t.Fatal("anonymous request unexpectedly had a subject")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestAuthInterceptorRejectsMalformedBearer(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "token nope"))
	interceptor := authInterceptor(func(ctx context.Context, token string) (string, error) {
		t.Fatalf("resolver called for malformed bearer token %q", token)
		return "", nil
	})
	_, err := interceptor(ctx, "request", &grpc.UnaryServerInfo{FullMethod: "/gitslice.core.v1.SliceService/GetSlice"}, func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler called for malformed bearer token")
		return nil, nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("malformed bearer error = %v, want Unauthenticated", err)
	}
}

func TestAuthInterceptorAttachesResolvedSubject(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer token_123"))
	interceptor := authInterceptor(func(ctx context.Context, token string) (string, error) {
		if token != "token_123" {
			t.Fatalf("resolver token = %q, want token_123", token)
		}
		return "user_alice", nil
	})
	_, err := interceptor(ctx, "request", &grpc.UnaryServerInfo{FullMethod: "/gitslice.core.v1.SliceService/GetSlice"}, func(ctx context.Context, req any) (any, error) {
		subjectID, ok := authctx.SubjectID(ctx)
		if !ok || subjectID != "user_alice" {
			t.Fatalf("subject = %q, %t; want user_alice, true", subjectID, ok)
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConnectAuthMiddlewareAttachesResolvedSubject(t *testing.T) {
	handler := connectAuthMiddleware(func(ctx context.Context, token string) (string, error) {
		if token != "token_123" {
			t.Fatalf("resolver token = %q, want token_123", token)
		}
		return "user_alice", nil
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subjectID, ok := authctx.SubjectID(r.Context())
		if !ok || subjectID != "user_alice" {
			t.Fatalf("subject = %q, %t; want user_alice, true", subjectID, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/gitslice.core.v1.SliceService/GetSlice", nil)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer token_123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

func TestConnectAuthMiddlewareRejectsMalformedBearer(t *testing.T) {
	handler := connectAuthMiddleware(func(ctx context.Context, token string) (string, error) {
		t.Fatalf("resolver called for malformed bearer token %q", token)
		return "", nil
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler called for malformed bearer token")
	}))

	req := httptest.NewRequest(http.MethodPost, "/gitslice.core.v1.SliceService/GetSlice", nil)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token nope")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestConnectHTTPHandlerServesAPI(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	tracker, err := analytics.New("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Fatal(err)
		}
	})
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, tracker)
	resolve := func(ctx context.Context, token string) (string, error) {
		if token == "token_123" {
			return "user_alice", nil
		}
		return "", storage.ErrUnauthenticated
	}
	httpServer := httptest.NewServer(NewHTTPHandler(NewConnectHandler(resolve, handlers), nil, "http://web.test"))
	t.Cleanup(httpServer.Close)

	client := corev1connect.NewSliceServiceClient(httpServer.Client(), httpServer.URL)
	req := connect.NewRequest(&corev1.ListSlicesRequest{Account: "acme"})
	req.Header().Set("Authorization", "Bearer token_123")
	resp, err := client.ListSlices(context.Background(), req)
	if err != nil {
		t.Fatalf("Connect ListSlices: %v", err)
	}
	foundPayment := false
	for _, slice := range resp.Msg.GetSlices() {
		if slice.GetRef().GetAccount() == "acme" && slice.GetRef().GetSlice() == "payment" {
			foundPayment = true
			break
		}
	}
	if !foundPayment {
		t.Fatalf("Connect ListSlices response missing acme:payment: %#v", resp.Msg.GetSlices())
	}

	badReq := connect.NewRequest(&corev1.ListSlicesRequest{Account: "acme"})
	badReq.Header().Set("Authorization", "Bearer wrong")
	_, err = client.ListSlices(context.Background(), badReq)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("invalid Connect token error = %v, want unauthenticated", err)
	}
}

func TestConnectServesSecrets(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)
	resolve := func(ctx context.Context, token string) (string, error) {
		if token == "token_123" {
			return "user_alice", nil
		}
		return "", storage.ErrUnauthenticated
	}
	httpServer := httptest.NewServer(NewHTTPHandler(NewConnectHandler(resolve, handlers), nil, "http://web.test"))
	t.Cleanup(httpServer.Close)

	client := corev1connect.NewSecretServiceClient(httpServer.Client(), httpServer.URL)
	create := connect.NewRequest(&corev1.CreateSecretRequest{
		Account: "acme",
		Name:    "CI_TOKEN",
		Value:   "s3cret",
		Scope:   &corev1.SecretScope{Slices: []*corev1.SliceRef{{Account: "acme", Slice: "home"}}},
	})
	create.Header().Set("Authorization", "Bearer token_123")
	created, err := client.CreateSecret(context.Background(), create)
	if err != nil {
		t.Fatalf("Connect CreateSecret: %v", err)
	}
	list := connect.NewRequest(&corev1.ListSecretsRequest{Account: "acme"})
	list.Header().Set("Authorization", "Bearer token_123")
	listed, err := client.ListSecrets(context.Background(), list)
	if err != nil {
		t.Fatalf("Connect ListSecrets: %v", err)
	}
	if len(listed.Msg.GetSecrets()) != 1 || listed.Msg.Secrets[0].GetId() != created.Msg.GetId() {
		t.Fatalf("listed = %+v", listed.Msg.GetSecrets())
	}
	anonymous := connect.NewRequest(&corev1.ListSecretsRequest{Account: "acme"})
	if _, err := client.ListSecrets(context.Background(), anonymous); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous ListSecrets: %v", err)
	}
}

func TestConnectServesWebhooks(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
		Webhooks:   mem.Webhooks,
	}, mem.Objects, nil)
	resolve := func(ctx context.Context, token string) (string, error) {
		if token == "token_123" {
			return "user_alice", nil
		}
		return "", storage.ErrUnauthenticated
	}
	httpServer := httptest.NewServer(NewHTTPHandler(NewConnectHandler(resolve, handlers), nil, "http://web.test"))
	t.Cleanup(httpServer.Close)

	client := corev1connect.NewWebhookServiceClient(httpServer.Client(), httpServer.URL)
	create := connect.NewRequest(&corev1.CreateWebhookRequest{
		Slice:  &corev1.SliceRef{Account: "acme", Slice: "home"},
		Url:    "https://hooks.example.com/gitslice",
		Events: []string{"tag.created"},
	})
	create.Header().Set("Authorization", "Bearer token_123")
	created, err := client.CreateWebhook(context.Background(), create)
	if err != nil {
		t.Fatalf("Connect CreateWebhook: %v", err)
	}
	list := connect.NewRequest(&corev1.ListWebhooksRequest{Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	list.Header().Set("Authorization", "Bearer token_123")
	listed, err := client.ListWebhooks(context.Background(), list)
	if err != nil {
		t.Fatalf("Connect ListWebhooks: %v", err)
	}
	if len(listed.Msg.GetWebhooks()) != 1 || listed.Msg.Webhooks[0].GetId() != created.Msg.GetId() {
		t.Fatalf("listed = %+v", listed.Msg.GetWebhooks())
	}
	anonymous := connect.NewRequest(&corev1.ListWebhooksRequest{Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if _, err := client.ListWebhooks(context.Background(), anonymous); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous ListWebhooks: %v", err)
	}
}

func TestCombinedServerHandlerTracksActivityInsideH2C(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)
	resolve := func(ctx context.Context, token string) (string, error) {
		if token == "token_123" {
			return "user_alice", nil
		}
		return "", storage.ErrUnauthenticated
	}

	gate := activity.NewIdle(40 * time.Millisecond)
	limiters := newServerLimiters(Config{RateLimitDisabled: true})
	grpcSrv := newGRPCServer(resolve, handlers, limiters)
	t.Cleanup(grpcSrv.Stop)
	apiHandler := newConnectHandler(resolve, handlers, limiters)
	gatewayMux := newHTTPHandler(apiHandler, nil, "*", Config{RateLimitDisabled: true}, true, limiters)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	httpSrv := &http.Server{
		Handler: newCombinedServerHandler(grpcSrv, gatewayMux, gate),
	}
	t.Cleanup(func() { _ = httpSrv.Close() })
	go func() { _ = httpSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// 1. gRPC Health/Check over h2c and HTTP probes (/healthz, /readyz, /metrics, OPTIONS) must NOT wake an idle gate.
	healthClient := healthv1.NewHealthClient(conn)
	if _, err := healthClient.Check(context.Background(), &healthv1.HealthCheckRequest{}); err != nil {
		t.Fatalf("Health.Check: %v", err)
	}

	h2cClient := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
	baseURL := "http://" + lis.Addr().String()
	for _, probe := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/readyz"},
		{http.MethodGet, "/metrics"},
		{http.MethodOptions, "/gitslice.core.v1.SliceService/ListSlices"},
	} {
		req, err := http.NewRequest(probe.method, baseURL+probe.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := h2cClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", probe.method, probe.path, err)
		}
		_ = resp.Body.Close()
	}

	if gate.Awake() {
		t.Fatal("gate woke on health/metrics/OPTIONS traffic over h2c; want idle")
	}

	// 2. Real gRPC unary RPC over the same h2c connection wakes the gate, and once finished,
	// the persistent h2c connection does NOT hold inFlight > 0 (gate returns to idle after idleAfter).
	sliceClient := corev1.NewSliceServiceClient(conn)
	authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer token_123")
	if _, err := sliceClient.ListSlices(authCtx, &corev1.ListSlicesRequest{Account: "acme"}); err != nil {
		t.Fatalf("gRPC ListSlices over h2c: %v", err)
	}
	if !gate.Awake() {
		t.Fatal("gate did not wake after real gRPC RPC over h2c")
	}

	time.Sleep(60 * time.Millisecond)
	if gate.Awake() {
		t.Fatal("gate stayed awake past idleAfter because h2c connection held inFlight; want idle")
	}
}

type deadlineTrackingResponseWriter struct {
	http.ResponseWriter
	readDeadlines  int
	writeDeadlines int
}

func (w *deadlineTrackingResponseWriter) SetReadDeadline(time.Time) error {
	w.readDeadlines++
	return nil
}

func (w *deadlineTrackingResponseWriter) SetWriteDeadline(time.Time) error {
	w.writeDeadlines++
	return nil
}

func TestIsStreamingHTTPRequestAndNonStreamingDeadlines(t *testing.T) {
	wrapped := withNonStreamingDeadlines(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), 5*time.Second, 10*time.Second)

	tests := []struct {
		name          string
		path          string
		headers       map[string]string
		wantStreaming bool
	}{
		{
			name:          "git smart-HTTP path is streaming",
			path:          "/git/acme/repo.git/git-upload-pack",
			wantStreaming: true,
		},
		{
			name:          "grpc content-type is streaming",
			path:          "/gitslice.core.v1.SliceService/ListSlices",
			headers:       map[string]string{"Content-Type": "application/grpc+proto"},
			wantStreaming: true,
		},
		{
			name:          "connect streaming envelope content-type is streaming",
			path:          "/gitslice.core.v1.AgentService/StreamConversation",
			headers:       map[string]string{"Content-Type": "application/connect+json"},
			wantStreaming: true,
		},
		{
			name:          "SSE accept header is streaming",
			path:          "/events",
			headers:       map[string]string{"Accept": "text/event-stream"},
			wantStreaming: true,
		},
		{
			name:          "known streaming RPC path is streaming",
			path:          "/gitslice.core.v1.RepositoryService/Checkout",
			headers:       map[string]string{"Content-Type": "application/proto"},
			wantStreaming: true,
		},
		{
			name:          "unary Connect JSON RPC gets deadlines",
			path:          "/gitslice.core.v1.SliceService/ListSlices",
			headers:       map[string]string{"Content-Type": "application/json"},
			wantStreaming: false,
		},
		{
			name:          "social image GET gets deadlines",
			path:          "/og/acme/repo.png",
			wantStreaming: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if got := isStreamingHTTPRequest(req); got != tt.wantStreaming {
				t.Fatalf("isStreamingHTTPRequest() = %v, want %v", got, tt.wantStreaming)
			}
			rec := &deadlineTrackingResponseWriter{ResponseWriter: httptest.NewRecorder()}
			wrapped.ServeHTTP(rec, req)
			wantCount := 1
			if tt.wantStreaming {
				wantCount = 0
			}
			if rec.readDeadlines != wantCount || rec.writeDeadlines != wantCount {
				t.Fatalf("deadlines set = (read=%d, write=%d), want (%d, %d)",
					rec.readDeadlines, rec.writeDeadlines, wantCount, wantCount)
			}
		})
	}
}
