package rpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/server"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRPCControllerApprovalsAndHumanGateWithPostgres(t *testing.T) {
	ts := startRPCServerWith(t, func(cfg *server.Config) {
		cfg.Auth.AgentSignupEnabled = true
	})
	conn := dialTestGRPC(t, ts.addr)
	clients := newTestCoreClients(conn)
	auth := corev1.NewAuthServiceClient(conn)
	slices := corev1.NewSliceServiceClient(conn)

	aliceToken := ts.loginViaGRPC(t, "alice")
	aliceCtx := grpcAuthContext(aliceToken)

	mintVerified := func(subject, email string) (string, context.Context) {
		t.Helper()
		token, err := servicetoken.MintVerifiedEmail(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		ctx := grpcAuthContext(token)
		st, err := auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return st.SubjectId, ctx
	}

	humanA, humanACtx := mintVerified("rpc_ctrl_a", "rpc-a@example.com")
	humanB, humanBCtx := mintVerified("rpc_ctrl_b", "rpc-b@example.com")
	ts.grantAccountRole(t, humanA, "acme", "admin")
	ts.grantAccountRole(t, humanB, "acme", "writer")

	agentA, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "rpc-agent-a", OwnerEmail: "rpc-a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	agentB, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "rpc-agent-b", OwnerEmail: "rpc-b@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	agentUnclaimed, err := auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{Username: "rpc-agent-unclaimed", OwnerEmail: "nobody@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := auth.AcceptClaim(humanACtx, &corev1.AcceptClaimRequest{AgentSubjectId: agentA.SubjectId}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AcceptClaim(humanBCtx, &corev1.AcceptClaimRequest{AgentSubjectId: agentB.SubjectId}); err != nil {
		t.Fatal(err)
	}
	ts.grantAccountRole(t, agentA.SubjectId, "acme", "writer")
	ts.grantAccountRole(t, agentB.SubjectId, "acme", "writer")
	ts.grantAccountRole(t, agentUnclaimed.SubjectId, "acme", "writer")

	agentACtx := grpcAuthContext(agentA.ApiKey)
	agentBCtx := grpcAuthContext(agentB.ApiKey)
	unclaimedCtx := grpcAuthContext(agentUnclaimed.ApiKey)

	// Reject negative required_human_approvals on CreateSlice.
	if _, err := slices.CreateSlice(aliceCtx, &corev1.CreateSliceRequest{
		Ref:                    &corev1.SliceRef{Account: "acme", Slice: "rpc-bad-human"},
		IncludedPaths:          []string{"/acme/payment"},
		Visibility:             "private",
		RequiredHumanApprovals: -1,
	}); grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSlice negative required_human_approvals code = %v, want InvalidArgument", grpcstatus.Code(err))
	}

	// Slice with required_approvals=1, required_human_approvals=1.
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "rpc-human-gate"}
	created, err := slices.CreateSlice(aliceCtx, &corev1.CreateSliceRequest{
		Ref:                    sliceRef,
		IncludedPaths:          []string{"/acme/payment"},
		Visibility:             "private",
		RequiredApprovals:      1,
		RequiredHumanApprovals: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetDefinition().GetRequiredHumanApprovals() != 1 {
		t.Fatalf("created RequiredHumanApprovals = %d, want 1", created.GetDefinition().GetRequiredHumanApprovals())
	}

	// Reject negative on UpdateSliceDefinition, then update roundtrip and set back to 1.
	if _, err := slices.UpdateSliceDefinition(aliceCtx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                created.GetId(),
		ExpectedDefinitionHash: created.GetDefinitionHash(),
		Definition: &corev1.SliceDefinition{
			IncludedPaths:          []string{"/acme/payment"},
			Visibility:             "private",
			RequiredApprovals:      1,
			RequiredHumanApprovals: -1,
		},
	}); grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("UpdateSliceDefinition negative code = %v, want InvalidArgument", grpcstatus.Code(err))
	}

	// AgentA authors a changeset on rpc-human-gate.
	csID, psID := createDirectPatchsetForSlice(t, agentACtx, clients, sliceRef, "/acme/payment/rpc_human_gate.txt", "v1\n", "rpc human gate")

	// Unclaimed agent and owner humanA do NOT satisfy required_approvals=1.
	if _, err := clients.changeset.ApproveChangeset(unclaimedCtx, &corev1.ApproveChangesetRequest{ChangesetId: csID}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.ApproveChangeset(humanACtx, &corev1.ApproveChangesetRequest{ChangesetId: csID}); err != nil {
		t.Fatal(err)
	}
	_, err = clients.changeset.SubmitChangeset(agentACtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               csID,
		ExpectedCurrentPatchsetId: psID,
	})
	assertSubmitBlocked(t, err, "requires 1 distinct non-author approval(s), has 0")

	// Claimed agentB satisfies required_approvals=1, but still blocks on required_human_approvals=1.
	if _, err := clients.changeset.ApproveChangeset(agentBCtx, &corev1.ApproveChangesetRequest{ChangesetId: csID}); err != nil {
		t.Fatal(err)
	}
	_, err = clients.changeset.SubmitChangeset(agentACtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               csID,
		ExpectedCurrentPatchsetId: psID,
	})
	wantHumanBlock := "required human approvals not satisfied: requires 1 human approval(s), has 0"
	assertSubmitBlocked(t, err, wantHumanBlock)
	assertChangesetBlockedReason(t, aliceCtx, clients.changeset, csID, wantHumanBlock)

	// HumanB approves -> clears block and SubmitChangeset succeeds.
	if _, err := clients.changeset.ApproveChangeset(humanBCtx, &corev1.ApproveChangesetRequest{ChangesetId: csID}); err != nil {
		t.Fatal(err)
	}
	csAfterApprove, err := clients.changeset.GetChangeset(aliceCtx, &corev1.GetChangesetRequest{ChangesetId: csID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(csAfterApprove.GetSubmitBlockedReason()) != "" {
		t.Fatalf("SubmitBlockedReason after human approval = %q, want cleared", csAfterApprove.GetSubmitBlockedReason())
	}
	if _, err := clients.changeset.SubmitChangeset(agentACtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               csID,
		ExpectedCurrentPatchsetId: psID,
	}); err != nil {
		t.Fatalf("SubmitChangeset after human approval failed: %v", err)
	}
}
