package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAgentClaimWithRequestedSliceGrant(t *testing.T) {
	ctx := context.Background()
	stores, h := newMemoryHandlers()
	h.Auth.AgentSignupEnabled = true

	ownerID, err := stores.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "svc-owner", "owner@example.com")
	if err != nil {
		t.Fatalf("EnsureExternalSubject: %v", err)
	}
	if err := stores.Auth.SetVerifiedEmails(ctx, ownerID, storage.ProviderService, []string{"owner@example.com"}); err != nil {
		t.Fatalf("SetVerifiedEmails: %v", err)
	}
	ownerCtx := authctx.WithPrincipal(ctx, authctx.Principal{SubjectID: ownerID, SubjectKind: "user", ControllerSubjectID: ownerID})
	if _, err := h.Auth.ChooseUsername(ownerCtx, &corev1.ChooseUsernameRequest{Username: "alice"}); err != nil {
		t.Fatalf("ChooseUsername: %v", err)
	}
	projSlice, err := stores.Slices.Create(ctx, ownerID, &corev1.SliceRef{Account: "alice", Slice: "proj"}, []string{"/alice/proj"}, "private", 0, nil)
	if err != nil {
		t.Fatalf("CreateSlice: %v", err)
	}

	reg, err := h.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{
		Username:       "alice-bot",
		OwnerEmail:     "owner@example.com",
		RequestedSlice: &corev1.SliceRef{Account: "alice", Slice: "proj"},
		RequestedRole:  "writer",
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	claims, err := h.Auth.ListPendingClaims(ownerCtx, &corev1.ListPendingClaimsRequest{})
	if err != nil {
		t.Fatalf("ListPendingClaims: %v", err)
	}
	if len(claims.GetClaims()) != 1 {
		t.Fatalf("got %d claims, want 1", len(claims.GetClaims()))
	}
	pc := claims.GetClaims()[0]
	if pc.GetRequestedSlice().GetAccount() != "alice" || pc.GetRequestedSlice().GetSlice() != "proj" || pc.GetRequestedRole() != "writer" {
		t.Fatalf("unexpected requested slice in claim: %+v", pc)
	}

	accepted, err := h.Auth.AcceptClaim(ownerCtx, &corev1.AcceptClaimRequest{
		AgentSubjectId:      reg.GetSubjectId(),
		GrantRequestedSlice: true,
	})
	if err != nil {
		t.Fatalf("AcceptClaim: %v", err)
	}
	if accepted.GetAccount() != "alice-bot" {
		t.Fatalf("got account %q, want alice-bot", accepted.GetAccount())
	}
	if accepted.GetGrantedSlice().GetAccount() != "alice" || accepted.GetGrantedSlice().GetSlice() != "proj" || accepted.GetGrantedRole() != "writer" {
		t.Fatalf("expected granted slice alice/proj (writer), got %+v / %q", accepted.GetGrantedSlice(), accepted.GetGrantedRole())
	}

	// Verify the agent can now read the private slice.
	agentPrincipal, err := stores.Auth.AuthenticateToken(ctx, reg.GetApiKey())
	if err != nil {
		t.Fatalf("AuthenticateToken: %v", err)
	}
	agentCtx := authctx.WithPrincipal(ctx, *agentPrincipal)
	if _, err := h.Slice.GetSlice(agentCtx, &corev1.GetSliceRequest{
		SliceId: projSlice.GetId(),
	}); err != nil {
		t.Fatalf("expected claimed agent with granted slice to access alice/proj: %v", err)
	}
}

func TestAgentInviteLifecycleAndSingleUse(t *testing.T) {
	ctx := context.Background()
	stores, h := newMemoryHandlers()

	ownerID, err := stores.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "svc-bob", "bob@example.com")
	if err != nil {
		t.Fatalf("EnsureExternalSubject: %v", err)
	}
	ownerCtx := authctx.WithPrincipal(ctx, authctx.Principal{SubjectID: ownerID, SubjectKind: "user", ControllerSubjectID: ownerID})
	if _, err := h.Auth.ChooseUsername(ownerCtx, &corev1.ChooseUsernameRequest{Username: "bobby"}); err != nil {
		t.Fatalf("ChooseUsername: %v", err)
	}
	appSlice, err := stores.Slices.Create(ctx, ownerID, &corev1.SliceRef{Account: "bobby", Slice: "app"}, []string{"/bobby/app"}, "private", 0, nil)
	if err != nil {
		t.Fatalf("CreateSlice: %v", err)
	}

	created, err := h.Auth.CreateAgentInvite(ownerCtx, &corev1.CreateAgentInviteRequest{
		Slice:             &corev1.SliceRef{Account: "bobby", Slice: "app"},
		Role:              "writer",
		SuggestedUsername: "bobby-coder",
		ExpiresInSeconds:  3600,
	})
	if err != nil {
		t.Fatalf("CreateAgentInvite: %v", err)
	}
	if !strings.HasPrefix(created.GetToken(), storage.AgentInviteTokenPrefix) {
		t.Fatalf("expected token with prefix %q, got %q", storage.AgentInviteTokenPrefix, created.GetToken())
	}

	// Join using the invite token (unauthenticated).
	joined, err := h.Auth.JoinAgentInvite(ctx, &corev1.JoinAgentInviteRequest{
		Token: created.GetToken(),
	})
	if err != nil {
		t.Fatalf("JoinAgentInvite: %v", err)
	}
	if joined.GetAccount() != "bobby-coder" || joined.GetRole() != "writer" {
		t.Fatalf("unexpected JoinAgentInviteResponse: %+v", joined)
	}
	if !strings.HasPrefix(joined.GetApiKey(), storage.APIKeyPrefix) {
		t.Fatalf("expected API key, got %q", joined.GetApiKey())
	}

	// Reusing the same single-use invite token must fail.
	if _, err := h.Auth.JoinAgentInvite(ctx, &corev1.JoinAgentInviteRequest{
		Token:    created.GetToken(),
		Username: "bobby-coder-2",
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated on reused invite token, got %v", err)
	}

	// Joined agent can access bobby/app immediately and is owned by bobby.
	agentPrincipal, err := stores.Auth.AuthenticateToken(ctx, joined.GetApiKey())
	if err != nil {
		t.Fatalf("AuthenticateToken: %v", err)
	}
	agentCtx := authctx.WithPrincipal(ctx, *agentPrincipal)
	if _, err := h.Slice.GetSlice(agentCtx, &corev1.GetSliceRequest{
		SliceId: appSlice.GetId(),
	}); err != nil {
		t.Fatalf("joined agent should access bobby/app: %v", err)
	}

	// Agent cannot mint invites for itself.
	if _, err := h.Auth.CreateAgentInvite(agentCtx, &corev1.CreateAgentInviteRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for agent CreateAgentInvite, got %v", err)
	}

	// Revoked invite cannot be redeemed.
	inv2, err := h.Auth.CreateAgentInvite(ownerCtx, &corev1.CreateAgentInviteRequest{})
	if err != nil {
		t.Fatalf("CreateAgentInvite 2: %v", err)
	}
	if _, err := h.Auth.RevokeAgentInvite(ownerCtx, &corev1.RevokeAgentInviteRequest{InviteId: inv2.GetInvite().GetId()}); err != nil {
		t.Fatalf("RevokeAgentInvite: %v", err)
	}
	if _, err := h.Auth.JoinAgentInvite(ctx, &corev1.JoinAgentInviteRequest{
		Token:    inv2.GetToken(),
		Username: "should-fail",
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated on revoked invite token, got %v", err)
	}
}
