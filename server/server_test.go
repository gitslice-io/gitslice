package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/proto/core/v1/corev1connect"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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
