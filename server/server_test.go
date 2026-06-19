package server

import (
	"context"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
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
