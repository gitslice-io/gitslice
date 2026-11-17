package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	corev1connect "gitslice.io/gitslice/proto/core/v1/corev1connect"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func testJWTWithExp(exp time.Time) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	pay := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"user_1","exp":%d}`, exp.Unix())))
	return hdr + "." + pay + ".sig"
}

func TestCachedPrincipalResolverSkipsAPIKeysAndSessions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	var calls atomic.Int32
	resolver := cachedPrincipalResolver(func(_ context.Context, token string) (*authctx.Principal, error) {
		n := calls.Add(1)
		return &authctx.Principal{
			SubjectID:     "user_alice",
			KeyID:         storage.APIKeyPrefix + fmt.Sprintf("%d", n),
			HasSliceScope: true,
			SliceIDs:      []string{"sl_1"},
			MaxRole:       "reader",
		}, nil
	}, func() time.Time { return now })

	apiKey := storage.APIKeyPrefix + "secret"
	if _, err := resolver(context.Background(), apiKey); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver(context.Background(), apiKey); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("API keys must not be cached: calls = %d, want 2", calls.Load())
	}

	// Non-JWT opaque session token must also not be cached.
	session := "sess_opaque_123"
	if _, err := resolver(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("session tokens must not be cached: calls = %d, want 4", calls.Load())
	}

	// Signed JWT IS cached.
	jwtTok := testJWTWithExp(now.Add(10 * time.Minute))
	p1, err := resolver(context.Background(), jwtTok)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := resolver(context.Background(), jwtTok)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 || p1.KeyID != p2.KeyID {
		t.Fatalf("JWT should be cached: calls = %d", calls.Load())
	}
}

func TestGRPCAndConnectEnforcePrincipalKeyGrants(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	owner, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, owner, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{owner}, owner); err != nil {
		t.Fatal(err)
	}
	allowed, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "allowed"}, []string{"/acme/allowed"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.Create(ctx, owner, &corev1.SliceRef{Account: "acme", Slice: "public-other"}, []string{"/acme/public-other"}, "public", 0, nil); err != nil {
		t.Fatal(err)
	}

	_, secret, _, _, err := mem.Auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:       owner,
		Name:          "scoped-reader",
		HasSliceScope: true,
		SliceIDs:      []string{allowed.Id},
		MaxRole:       "reader",
	})
	if err != nil {
		t.Fatal(err)
	}

	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)

	grpcSrv := NewPrincipalGRPCServer(mem.Auth.AuthenticateToken, handlers, Config{RateLimitDisabled: true})
	t.Cleanup(grpcSrv.Stop)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+secret)
	sliceClient := corev1.NewSliceServiceClient(conn)
	if _, err := sliceClient.ResolveSlice(authCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "allowed"},
	}); err != nil {
		t.Fatalf("gRPC ResolveSlice allowed = %v", err)
	}
	if _, err := sliceClient.ResolveSlice(authCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "public-other"},
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("gRPC ResolveSlice out-of-scope public = %v, want PermissionDenied", err)
	}
	if _, err := corev1.NewChangesetServiceClient(conn).CreateChangeset(authCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "allowed"},
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("gRPC CreateChangeset with reader key = %v, want PermissionDenied", err)
	}
	if _, err := corev1.NewAuthServiceClient(conn).CreateOrganization(authCtx, &corev1.CreateOrganizationRequest{
		Slug: "blocked-org",
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("gRPC CreateOrganization with scoped key = %v, want PermissionDenied", err)
	}

	// Verify Connect HTTP middleware enforces identical principal grants.
	connectSrv := httptest.NewServer(NewPrincipalConnectHandler(mem.Auth.AuthenticateToken, handlers))
	t.Cleanup(connectSrv.Close)

	connectSlices := corev1connect.NewSliceServiceClient(http.DefaultClient, connectSrv.URL)
	reqAllowed := connect.NewRequest(&corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "allowed"},
	})
	reqAllowed.Header().Set("Authorization", "Bearer "+secret)
	if _, err := connectSlices.ResolveSlice(ctx, reqAllowed); err != nil {
		t.Fatalf("Connect ResolveSlice allowed = %v", err)
	}
	reqDenied := connect.NewRequest(&corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "public-other"},
	})
	reqDenied.Header().Set("Authorization", "Bearer "+secret)
	if _, err := connectSlices.ResolveSlice(ctx, reqDenied); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("Connect ResolveSlice out-of-scope public = %v, want PermissionDenied", err)
	}
}
