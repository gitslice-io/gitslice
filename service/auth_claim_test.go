package service

import (
	"context"
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeClerkUsers struct {
	emails map[string][]string
	err    error
}

func (f fakeClerkUsers) VerifiedEmails(ctx context.Context, userID string) ([]string, error) {
	return f.emails[userID], f.err
}

func TestClaimFlowVerifiesEmailWithClerk(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	handlers.Auth.AgentSignupEnabled = true
	ctx := context.Background()

	agent, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "release-bot", OwnerEmail: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	human, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "user_clerk_1", "")
	if err != nil {
		t.Fatal(err)
	}
	humanCtx := authctx.WithSubjectID(ctx, human)

	// Without a Clerk client the server cannot verify the caller's email.
	if _, err := handlers.Auth.ListPendingClaims(humanCtx, &corev1.ListPendingClaimsRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no clerk client code = %v; want FailedPrecondition", status.Code(err))
	}
	handlers.Auth.ClerkUsers = fakeClerkUsers{err: errors.New("down")}
	if _, err := handlers.Auth.ListPendingClaims(humanCtx, &corev1.ListPendingClaimsRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("clerk outage code = %v; want Unavailable", status.Code(err))
	}

	handlers.Auth.ClerkUsers = fakeClerkUsers{emails: map[string][]string{"user_clerk_1": {"Owner@example.com"}}}
	list, err := handlers.Auth.ListPendingClaims(humanCtx, &corev1.ListPendingClaimsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Claims) != 1 || list.Claims[0].AgentSubjectId != agent.SubjectId || list.Claims[0].Account != "release-bot" || list.Claims[0].CreatedAt == "" {
		t.Fatalf("ListPendingClaims = %+v", list.Claims)
	}
	if _, err := handlers.Auth.AcceptClaim(humanCtx, &corev1.AcceptClaimRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty agent id code = %v; want InvalidArgument", status.Code(err))
	}
	accepted, err := handlers.Auth.AcceptClaim(humanCtx, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId})
	if err != nil || accepted.Account != "release-bot" {
		t.Fatalf("AcceptClaim = %+v, %v", accepted, err)
	}

	owned, err := handlers.Auth.ListOwnedAgents(humanCtx, &corev1.ListOwnedAgentsRequest{})
	if err != nil || len(owned.Agents) != 1 || owned.Agents[0].Account != "release-bot" || owned.Agents[0].ClaimedAt == "" || owned.Agents[0].LastActiveAt != "" {
		t.Fatalf("ListOwnedAgents = %+v, %v", owned, err)
	}

	authStatus, err := handlers.Auth.GetAuthStatus(humanCtx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !authStatus.NeedsUsername || len(authStatus.Accounts) != 1 || authStatus.Accounts[0] != "release-bot" {
		t.Fatalf("claimer status = %+v; want the claimed account and needs_username", authStatus)
	}
	if len(authStatus.Memberships) != 1 || authStatus.Memberships[0].Kind != "agent" || authStatus.Memberships[0].Role != "owner" {
		t.Fatalf("claimer memberships = %+v; want the agent's account, owned", authStatus.Memberships)
	}

	// The agent itself has no verified emails, so it can never claim.
	agentCtx := authctx.WithSubjectID(ctx, agent.SubjectId)
	if list, err := handlers.Auth.ListPendingClaims(agentCtx, &corev1.ListPendingClaimsRequest{}); err != nil || len(list.Claims) != 0 {
		t.Fatalf("agent claims = %+v, %v", list, err)
	}
}

func TestClaimRPCsRequireSubject(t *testing.T) {
	_, handlers := newMemoryHandlers()
	if _, err := handlers.Auth.ListPendingClaims(context.Background(), &corev1.ListPendingClaimsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v; want Unauthenticated", status.Code(err))
	}
	if _, err := handlers.Auth.AcceptClaim(context.Background(), &corev1.AcceptClaimRequest{AgentSubjectId: "agent_x"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v; want Unauthenticated", status.Code(err))
	}
}
