package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestServiceControllerApprovalsAndHumanGateEndToEnd(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	handlers.Auth.AgentSignupEnabled = true
	ctx := context.Background()
	seedData := []byte("seed\n")
	seedHash := objectid.RawContentHash(seedData)
	mem.PutObject(filesystem.BlobKey(seedHash), seedData)
	mem.PutCommitWithFiles("commit_svc_controller_seed", []storage.FileEntry{
		{Path: "/acme/svc-agent-only/seed.txt", BlobID: objectid.BlobID(seedData), ContentHash: seedHash, Mode: 0o100644, Size: int64(len(seedData))},
		{Path: "/acme/svc-human-gate/seed.txt", BlobID: objectid.BlobID(seedData), ContentHash: seedHash, Mode: 0o100644, Size: int64(len(seedData))},
	}, []string{"/acme/svc-agent-only/seed.txt", "/acme/svc-human-gate/seed.txt"})

	humanA, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_a", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	humanB, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_b", "b@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mem.AddAccountRole(humanA, "acme", "admin")
	mem.AddAccountRole(humanB, "acme", "writer")

	handlers.Auth.ClerkUsers = fakeClerkUsers{emails: map[string][]string{
		"clerk_a": {"a@example.com"},
		"clerk_b": {"b@example.com"},
	}}

	agentA, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "svc-agent-a", OwnerEmail: "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	agentB1, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "svc-agent-b1", OwnerEmail: "b@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	agentB2, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "svc-agent-b2", OwnerEmail: "b@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	agentUnclaimed, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "svc-agent-unclaimed", OwnerEmail: "nobody@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handlers.Auth.AcceptClaim(authctx.WithSubjectID(ctx, humanA), &corev1.AcceptClaimRequest{AgentSubjectId: agentA.SubjectId}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.AcceptClaim(authctx.WithSubjectID(ctx, humanB), &corev1.AcceptClaimRequest{AgentSubjectId: agentB1.SubjectId}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.AcceptClaim(authctx.WithSubjectID(ctx, humanB), &corev1.AcceptClaimRequest{AgentSubjectId: agentB2.SubjectId}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{agentA.SubjectId, agentB1.SubjectId, agentB2.SubjectId, agentUnclaimed.SubjectId} {
		mem.AddAccountRole(id, "acme", "writer")
	}

	humanACtx := authctx.WithSubjectID(ctx, humanA)
	humanBCtx := authctx.WithSubjectID(ctx, humanB)
	agentACtx := authctx.WithSubjectID(ctx, agentA.SubjectId)
	agentB1Ctx := authctx.WithSubjectID(ctx, agentB1.SubjectId)
	agentB2Ctx := authctx.WithSubjectID(ctx, agentB2.SubjectId)
	unclaimedCtx := authctx.WithSubjectID(ctx, agentUnclaimed.SubjectId)

	// Case 1: agent-to-agent approval from a different controller passes when required_human_approvals=0.
	sliceAgentOnly := &corev1.SliceRef{Account: "acme", Slice: "svc-agent-only"}
	if _, err := handlers.Slice.CreateSlice(humanACtx, &corev1.CreateSliceRequest{
		Ref:               sliceAgentOnly,
		IncludedPaths:     []string{"/acme/svc-agent-only"},
		Visibility:        "private",
		RequiredApprovals: 1,
	}); err != nil {
		t.Fatal(err)
	}

	createDraft := func(authorCtx context.Context, slice *corev1.SliceRef, p, content string) (*corev1.Changeset, *corev1.Patchset) {
		t.Helper()
		ref, err := handlers.Repository.GetRef(authorCtx, &corev1.GetRefRequest{})
		if err != nil {
			t.Fatal(err)
		}
		upload, err := handlers.Blob.UploadBlob(authorCtx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: slice})
		if err != nil {
			t.Fatal(err)
		}
		cs, err := handlers.Changeset.CreateChangeset(authorCtx, &corev1.CreateChangesetRequest{
			AuthoringSlice: slice,
			TargetRef:      storage.DefaultTargetRef,
			BaseCommitId:   ref.CommitId,
			Title:          "draft " + p,
		})
		if err != nil {
			t.Fatal(err)
		}
		ps, err := handlers.Changeset.UpdateChangeset(authorCtx, &corev1.UpdateChangesetRequest{
			ChangesetId:  cs.Id,
			BaseCommitId: ref.CommitId,
			FileEdits: []*corev1.FileEdit{{
				Op:          "add",
				Path:        p,
				BlobId:      upload.BlobId,
				ContentHash: upload.ContentHash,
				Mode:        0o100644,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return cs, ps
	}

	cs1, ps1 := createDraft(agentACtx, sliceAgentOnly, "/acme/svc-agent-only/a.txt", "v1\n")

	// Unclaimed agent and author's controller humanA cannot satisfy required_approvals=1.
	if _, err := handlers.Changeset.ApproveChangeset(unclaimedCtx, &corev1.ApproveChangesetRequest{ChangesetId: cs1.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.ApproveChangeset(humanACtx, &corev1.ApproveChangesetRequest{ChangesetId: cs1.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(agentACtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs1.Id,
		ExpectedCurrentPatchsetId: ps1.Id,
	}); err == nil || !strings.Contains(err.Error(), "requires 1 distinct non-author approval(s), has 0") {
		t.Fatalf("Submit after unclaimed + controller approval = %v, want has 0", err)
	}

	// Claimed agent under humanB satisfies required_approvals=1.
	if _, err := handlers.Changeset.ApproveChangeset(agentB1Ctx, &corev1.ApproveChangesetRequest{ChangesetId: cs1.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(agentACtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs1.Id,
		ExpectedCurrentPatchsetId: ps1.Id,
	}); err != nil {
		t.Fatalf("Submit after distinct-controller agent approval failed: %v", err)
	}

	// Case 2: required_human_approvals=1 rejects agent-only approvals (even from multiple distinct agents of humanB) until humanB approves.
	sliceHumanGate := &corev1.SliceRef{Account: "acme", Slice: "svc-human-gate"}
	if _, err := handlers.Slice.CreateSlice(humanACtx, &corev1.CreateSliceRequest{
		Ref:                    sliceHumanGate,
		IncludedPaths:          []string{"/acme/svc-human-gate"},
		Visibility:             "private",
		RequiredApprovals:      1,
		RequiredHumanApprovals: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cs2, ps2 := createDraft(agentACtx, sliceHumanGate, "/acme/svc-human-gate/b.txt", "v1\n")
	if _, err := handlers.Changeset.ApproveChangeset(agentB1Ctx, &corev1.ApproveChangesetRequest{ChangesetId: cs2.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.ApproveChangeset(agentB2Ctx, &corev1.ApproveChangesetRequest{ChangesetId: cs2.Id}); err != nil {
		t.Fatal(err)
	}
	wantHumanBlock := "required human approvals not satisfied: requires 1 human approval(s), has 0"
	if _, err := handlers.Changeset.SubmitChangeset(agentACtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs2.Id,
		ExpectedCurrentPatchsetId: ps2.Id,
	}); err == nil || !strings.Contains(err.Error(), wantHumanBlock) {
		t.Fatalf("Submit with agent approvals under human gate = %v, want %q", err, wantHumanBlock)
	}

	if _, err := handlers.Changeset.ApproveChangeset(humanBCtx, &corev1.ApproveChangesetRequest{ChangesetId: cs2.Id}); err != nil {
		t.Fatal(err)
	}
	gotCS2, err := handlers.Changeset.GetChangeset(humanACtx, &corev1.GetChangesetRequest{ChangesetId: cs2.Id})
	if err != nil {
		t.Fatal(err)
	}
	if gotCS2.GetSubmitBlockedReason() != "" {
		t.Fatalf("SubmitBlockedReason after human approval = %q, want cleared", gotCS2.GetSubmitBlockedReason())
	}
	if _, err := handlers.Changeset.SubmitChangeset(agentACtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs2.Id,
		ExpectedCurrentPatchsetId: ps2.Id,
	}); err != nil {
		t.Fatalf("Submit after human approval failed: %v", err)
	}
}
