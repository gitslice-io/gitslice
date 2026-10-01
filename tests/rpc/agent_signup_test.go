package rpc_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRegisterAgentWithPostgres(t *testing.T) {
	ts := startRPCServer(t)
	conn := dialTestGRPC(t, ts.addr)
	auth := corev1.NewAuthServiceClient(conn)

	// Unauthenticated: no bearer token on the context.
	resp, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{
		Username:    "release-bot",
		OwnerEmail:  " Owner@Example.com ",
		DisplayName: "Release bot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Account != "release-bot" || !strings.HasPrefix(resp.SubjectId, "agent_") || !strings.HasPrefix(resp.ApiKey, "gsk_") {
		t.Fatalf("RegisterAgent = subject %q account %q", resp.SubjectId, resp.Account)
	}

	ctx := grpcAuthContext(resp.ApiKey)
	status, err := auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if status.SubjectId != resp.SubjectId || len(status.Accounts) != 1 || status.Accounts[0] != "release-bot" {
		t.Fatalf("GetAuthStatus = %+v", status)
	}
	if _, err := corev1.NewSliceServiceClient(conn).ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "release-bot", Slice: "home"},
	}); err != nil {
		t.Fatalf("agent cannot resolve its home slice: %v", err)
	}

	if _, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "release-bot", OwnerEmail: "owner@example.com"}); grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("duplicate username code = %v; want FailedPrecondition", grpcstatus.Code(err))
	}
	if _, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "other-bot", OwnerEmail: "not-an-email"}); grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad email code = %v; want InvalidArgument", grpcstatus.Code(err))
	}
	if _, err := auth.GetAuthStatus(grpcAuthContext("gsk_"+strings.Repeat("0", 32)), &corev1.GetAuthStatusRequest{}); grpcstatus.Code(err) != codes.Unauthenticated {
		t.Fatalf("unknown key code = %v; want Unauthenticated", grpcstatus.Code(err))
	}

	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var kind, ownerEmail string
	var lastUsed sql.NullTime
	if err := db.QueryRow(`select kind from subjects where id = $1`, resp.SubjectId).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select owner_email from agent_registrations where subject_id = $1 and claimed_at is null`, resp.SubjectId).Scan(&ownerEmail); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select last_used_at from api_keys where subject_id = $1`, resp.SubjectId).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if kind != "agent" || ownerEmail != "owner@example.com" || !lastUsed.Valid {
		t.Fatalf("kind=%q owner_email=%q last_used_at valid=%v", kind, ownerEmail, lastUsed.Valid)
	}
	// Revoked keys stop working immediately.
	if _, err := db.Exec(`update api_keys set revoked_at = now() where subject_id = $1`, resp.SubjectId); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{}); grpcstatus.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked key code = %v; want Unauthenticated", grpcstatus.Code(err))
	}
}

func TestRegisterAgentIsRetryableWithPostgres(t *testing.T) {
	ts := startRPCServer(t)
	conn := dialTestGRPC(t, ts.addr)
	auth := corev1.NewAuthServiceClient(conn)
	req := &corev1.RegisterAgentRequest{
		Username:          "retry-bot",
		OwnerEmail:        "owner@example.com",
		RegistrationToken: strings.Repeat("t", 40),
	}
	first, err := auth.RegisterAgent(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a timeout after the server committed: the client never saw the
	// first key, so it retries with the same token.
	second, err := auth.RegisterAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if second.SubjectId != first.SubjectId || second.Account != "retry-bot" || second.ApiKey == first.ApiKey {
		t.Fatalf("retry = %+v; want the same agent with a new key", second)
	}
	if _, err := auth.GetAuthStatus(grpcAuthContext(first.ApiKey), &corev1.GetAuthStatusRequest{}); grpcstatus.Code(err) != codes.Unauthenticated {
		t.Fatalf("never-used first key code = %v; want Unauthenticated (revoked)", grpcstatus.Code(err))
	}
	if _, err := auth.GetAuthStatus(grpcAuthContext(second.ApiKey), &corev1.GetAuthStatusRequest{}); err != nil {
		t.Fatalf("resumed key: %v", err)
	}
	// Without the token the username is simply taken.
	if _, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "retry-bot", OwnerEmail: "owner@example.com"}); grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("tokenless duplicate code = %v; want FailedPrecondition", grpcstatus.Code(err))
	}
}
