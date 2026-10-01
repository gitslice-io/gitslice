package rpc_test

import (
	"context"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestAgentClaimWithPostgres(t *testing.T) {
	ts := startRPCServer(t)
	conn := dialTestGRPC(t, ts.addr)
	auth := corev1.NewAuthServiceClient(conn)
	slices := corev1.NewSliceServiceClient(conn)

	agent, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "claim-bot", OwnerEmail: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := grpcAuthContext(agent.ApiKey)

	mint := func(subject, email string, verified bool) context.Context {
		t.Helper()
		var token string
		var err error
		if verified {
			token, err = servicetoken.MintVerifiedEmail(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
		} else {
			token, err = servicetoken.Mint(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
		}
		if err != nil {
			t.Fatal(err)
		}
		return grpcAuthContext(token)
	}

	// Same email but not verified: nothing to claim.
	unverified := mint("svc_claim_unverified", "owner@example.com", false)
	if list, err := auth.ListPendingClaims(unverified, &corev1.ListPendingClaimsRequest{}); err != nil || len(list.Claims) != 0 {
		t.Fatalf("unverified claims = %+v, %v", list, err)
	}
	if _, err := auth.AcceptClaim(unverified, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId}); grpcstatus.Code(err) != codes.NotFound {
		t.Fatalf("unverified accept code = %v; want NotFound", grpcstatus.Code(err))
	}
	// The agent cannot claim itself.
	if _, err := auth.AcceptClaim(agentCtx, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId}); grpcstatus.Code(err) != codes.NotFound {
		t.Fatalf("agent self-claim code = %v; want NotFound", grpcstatus.Code(err))
	}

	human := mint("svc_claim_owner", "Owner@Example.com", true)
	list, err := auth.ListPendingClaims(human, &corev1.ListPendingClaimsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Claims) != 1 || list.Claims[0].AgentSubjectId != agent.SubjectId || list.Claims[0].Account != "claim-bot" || list.Claims[0].OwnerEmail != "owner@example.com" {
		t.Fatalf("ListPendingClaims = %+v", list.Claims)
	}
	accepted, err := auth.AcceptClaim(human, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId})
	if err != nil || accepted.Account != "claim-bot" {
		t.Fatalf("AcceptClaim = %+v, %v", accepted, err)
	}
	if _, err := auth.AcceptClaim(human, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId}); grpcstatus.Code(err) != codes.NotFound {
		t.Fatalf("second accept code = %v; want NotFound", grpcstatus.Code(err))
	}

	owned, err := auth.ListOwnedAgents(human, &corev1.ListOwnedAgentsRequest{})
	if err != nil || len(owned.Agents) != 1 {
		t.Fatalf("ListOwnedAgents = %+v, %v", owned, err)
	}
	if got := owned.Agents[0]; got.AgentSubjectId != agent.SubjectId || got.Account != "claim-bot" || got.RegisteredAt == "" || got.ClaimedAt == "" || got.LastActiveAt == "" {
		t.Fatalf("owned agent = %+v; want claimed and active (the agent used its key)", got)
	}
	if list, err := auth.ListOwnedAgents(unverified, &corev1.ListOwnedAgentsRequest{}); err != nil || len(list.Agents) != 0 {
		t.Fatalf("non-owner owned agents = %+v, %v", list, err)
	}

	// Co-ownership: both can reach the agent's home slice.
	home := &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "claim-bot", Slice: "home"}}
	if _, err := slices.ResolveSlice(human, home); err != nil {
		t.Fatalf("owner cannot resolve the claimed home slice: %v", err)
	}
	if _, err := slices.ResolveSlice(agentCtx, home); err != nil {
		t.Fatalf("agent lost access after the claim: %v", err)
	}

	// The claimed account is not the human's own: they still pick a username,
	// cannot take the agent's, and their own account sorts first afterwards.
	status, err := auth.GetAuthStatus(human, &corev1.GetAuthStatusRequest{})
	if err != nil || !status.NeedsUsername {
		t.Fatalf("claimer status = %+v, %v; want needs_username", status, err)
	}
	if _, err := auth.ChooseUsername(human, &corev1.ChooseUsernameRequest{Username: "claim-bot"}); grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("choosing the agent's username code = %v; want FailedPrecondition", grpcstatus.Code(err))
	}
	chosen, err := auth.ChooseUsername(human, &corev1.ChooseUsernameRequest{Username: "claim-owner"})
	if err != nil || chosen.Account != "claim-owner" {
		t.Fatalf("ChooseUsername = %+v, %v", chosen, err)
	}
	status, err = auth.GetAuthStatus(human, &corev1.GetAuthStatusRequest{})
	if err != nil || status.NeedsUsername || len(status.Accounts) != 2 || status.Accounts[0] != "claim-owner" {
		t.Fatalf("claimer status after username = %+v, %v", status, err)
	}
}
