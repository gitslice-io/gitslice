package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func commitSliceFile(t *testing.T, ctx context.Context, h *Handlers, mem *memory.Stores, slice *corev1.SliceRef, filePath, content string) string {
	t.Helper()
	ref, err := h.Repository.GetRef(ctx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := h.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: slice})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := h.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "seed " + filePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := h.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        filePath,
			BlobId:      blob.BlobId,
			ContentHash: blob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: ps.Id,
	}); err != nil {
		t.Fatalf("submit seed %s: %v", filePath, err)
	}
	if _, err := mem.Changesets.PublishPending(ctx, 16); err != nil {
		t.Fatal(err)
	}
	after, err := h.Repository.GetRef(ctx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	return after.CommitId
}

func proposeEdits(t *testing.T, ctx context.Context, h *Handlers, slice *corev1.SliceRef, baseCommit, title string, edits ...*corev1.FileEdit) (*corev1.Changeset, *corev1.Patchset) {
	t.Helper()
	cs, err := h.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   baseCommit,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := h.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommit,
		FileEdits:    edits,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs, ps
}

func upsertEdit(t *testing.T, ctx context.Context, h *Handlers, slice *corev1.SliceRef, p, content string) *corev1.FileEdit {
	t.Helper()
	blob, err := h.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: slice})
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.FileEdit{
		Op:          "upsert",
		Path:        p,
		BlobId:      blob.BlobId,
		ContentHash: blob.ContentHash,
		Mode:        0o100644,
	}
}

func TestPathOwnersReadFromLandedHeadNearestPrecedenceAndClearing(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "user_bobby", "bobby")
	carol := asUser(t, h, "user_carol", "carol")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "team"}); err != nil {
		t.Fatal(err)
	}
	for _, uname := range []string{"bobby", "carol"} {
		uCtx := bobby
		if uname == "carol" {
			uCtx = carol
		}
		if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "team", Username: uname, Role: "writer"}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Auth.RespondToInvitation(uCtx, &corev1.RespondToInvitationRequest{Account: "team", Accept: true}); err != nil {
			t.Fatal(err)
		}
	}

	// Seed root + sub-folder files so slices exist.
	data := []byte("init\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_init", []storage.FileEntry{{
		Path: "/team/app/api/v2/handler.go", BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100644, Size: int64(len(data)),
	}}, []string{"/team/app/api/v2/handler.go"})

	homeRef := &corev1.SliceRef{Account: "team", Slice: "home"}
	appRef := &corev1.SliceRef{Account: "team", Slice: "app"}
	if _, err := h.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref: appRef, IncludedPaths: []string{"/team/app"}, Visibility: "private",
	}); err != nil {
		t.Fatal(err)
	}

	// Land account-level owners.yaml requiring alice on everything, plus a nearer
	// /team/app/api/.gitslice/owners.yaml with a rule for v2/** (bobby), require:0 for
	// public/**, and NO rule for unmatched files inside /team/app/api.
	commitSliceFile(t, alice, h, mem, homeRef, "/team/.gitslice/owners.yaml",
		"version: 1\nowners:\n  - paths: [\"**\"]\n    owners: [\"alice\"]\n")
	headCommit := commitSliceFile(t, alice, h, mem, appRef, "/team/app/api/.gitslice/owners.yaml",
		"version: 1\nowners:\n  - paths: [\"public/**\"]\n    owners: [\"alice\"]\n    require: 0\n  - paths: [\"v2/**\"]\n    owners: [\"bobby\"]\n    require: 1\n")

	// 1. Subfolder file matching nearest rule v2/** needs bobby (NOT alice!).
	//    Carol also tries bundling a modified owners.yaml in the same changeset;
	//    landed owners.yaml STILL governs AND .gitslice/** also rejects carol.
	csBypass, psBypass := proposeEdits(t, carol, h, appRef, headCommit, "bypass attempt",
		upsertEdit(t, carol, h, appRef, "/team/app/api/v2/handler.go", "package v2\n"),
		upsertEdit(t, carol, h, appRef, "/team/app/api/.gitslice/owners.yaml", "version: 1\nowners:\n  - paths: [\"**\"]\n    owners: [\"carol\"]\n    require: 1\n"),
	)
	_, err := h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csBypass.Id, ExpectedCurrentPatchsetId: psBypass.Id})
	wantCode(t, err, codes.FailedPrecondition, "carol bundling own owners.yaml")

	// Normal edit to v2/** by carol sets SubmitBlockedReason ("path owner..."), cleared on bobby's approval!
	csV2, psV2 := proposeEdits(t, carol, h, appRef, headCommit, "v2 edit",
		upsertEdit(t, carol, h, appRef, "/team/app/api/v2/handler.go", "package v2\n// change\n"),
	)
	_, err = h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csV2.Id, ExpectedCurrentPatchsetId: psV2.Id})
	wantCode(t, err, codes.FailedPrecondition, "carol editing v2/** without bobby")
	if !strings.Contains(err.Error(), "path owner approvals not satisfied") || !strings.Contains(err.Error(), "/team/app/api/.gitslice/owners.yaml") {
		t.Fatalf("unexpected error: %v", err)
	}
	gotBlocked, err := h.Changeset.GetChangeset(carol, &corev1.GetChangesetRequest{ChangesetId: csV2.Id})
	if err != nil || !strings.HasPrefix(gotBlocked.GetSubmitBlockedReason(), "path owner") || !storage.IsRequirementBlock(gotBlocked.GetSubmitBlockedReason()) {
		t.Fatalf("SubmitBlockedReason = %q (%v), want path owner block", gotBlocked.GetSubmitBlockedReason(), err)
	}

	// Non-owner alice approving does NOT satisfy bobby-only rule on v2/**!
	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: csV2.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csV2.Id, ExpectedCurrentPatchsetId: psV2.Id})
	wantCode(t, err, codes.FailedPrecondition, "alice approving bobby-only v2/**")

	// Owner bobby approves -> clears SubmitBlockedReason and submit succeeds!
	if _, err := h.Changeset.ApproveChangeset(bobby, &corev1.ApproveChangesetRequest{ChangesetId: csV2.Id}); err != nil {
		t.Fatal(err)
	}
	gotCleared, err := h.Changeset.GetChangeset(carol, &corev1.GetChangesetRequest{ChangesetId: csV2.Id})
	if err != nil || gotCleared.GetSubmitBlockedReason() != "" {
		t.Fatalf("SubmitBlockedReason after bobby approval = %q (%v), want cleared", gotCleared.GetSubmitBlockedReason(), err)
	}
	if _, err := h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csV2.Id, ExpectedCurrentPatchsetId: psV2.Id}); err != nil {
		t.Fatalf("SubmitChangeset after bobby approval failed: %v", err)
	}
	if _, err := mem.Changesets.PublishPending(alice, 16); err != nil {
		t.Fatal(err)
	}
	refAfter, _ := h.Repository.GetRef(alice, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	headCommit = refAfter.CommitId

	// 2. Nearest file /team/app/api/.gitslice/owners.yaml beats /team/.gitslice/owners.yaml:
	//    - public/doc.md has require: 0 -> carol submits without alice.
	//    - notes.md has no matching rule in nearest file -> no fallback to /team/.gitslice/owners.yaml -> carol submits without alice.
	//    - /team/app/other.go has no nearer file -> falls back to /team/.gitslice/owners.yaml -> requires alice!
	csPublic, psPublic := proposeEdits(t, carol, h, appRef, headCommit, "public+unmatched",
		upsertEdit(t, carol, h, appRef, "/team/app/api/public/doc.md", "hi\n"),
		upsertEdit(t, carol, h, appRef, "/team/app/api/notes.md", "notes\n"),
	)
	if _, err := h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csPublic.Id, ExpectedCurrentPatchsetId: psPublic.Id}); err != nil {
		t.Fatalf("nearest owners.yaml unmatched/require:0 rejected: %v", err)
	}
	if _, err := mem.Changesets.PublishPending(alice, 16); err != nil {
		t.Fatal(err)
	}
	refAfter, _ = h.Repository.GetRef(alice, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	headCommit = refAfter.CommitId

	csFallback, psFallback := proposeEdits(t, carol, h, appRef, headCommit, "parent fallback",
		upsertEdit(t, carol, h, appRef, "/team/app/other.go", "package app\n"),
	)
	_, err = h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csFallback.Id, ExpectedCurrentPatchsetId: psFallback.Id})
	wantCode(t, err, codes.FailedPrecondition, "carol editing /team/app/other.go governed by /team/.gitslice/owners.yaml")
	if !strings.Contains(err.Error(), "/team/.gitslice/owners.yaml") {
		t.Fatalf("expected /team/.gitslice/owners.yaml in error, got: %v", err)
	}

	// 3. Invalid owners.yaml uploaded in UpdateChangeset is rejected immediately.
	csBad, err := h.Changeset.CreateChangeset(alice, &corev1.CreateChangesetRequest{
		AuthoringSlice: appRef, TargetRef: storage.DefaultTargetRef, BaseCommitId: headCommit, Title: "bad yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Changeset.UpdateChangeset(alice, &corev1.UpdateChangesetRequest{
		ChangesetId: csBad.Id, BaseCommitId: headCommit,
		FileEdits: []*corev1.FileEdit{upsertEdit(t, alice, h, appRef, "/team/app/.gitslice/owners.yaml", "version: 2\nowners: []\n")},
	})
	wantCode(t, err, codes.InvalidArgument, "UpdateChangeset invalid owners.yaml")
}

func TestPathOwnersGroupsControllersUnclaimedSelfOwnAndGitslice(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "user_bobby", "bobby")
	carol := asUser(t, h, "user_carol", "carol")
	admin2 := asUser(t, h, "user_admin2", "admin2")
	aliceID, _ := authctx.SubjectID(alice)
	bobbyID, _ := authctx.SubjectID(bobby)

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "corp"}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		name, role string
		ctx        context.Context
	}{
		{"bobby", "writer", bobby},
		{"carol", "writer", carol},
		{"admin2", "admin", admin2},
	} {
		if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "corp", Username: u.name, Role: u.role}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Auth.RespondToInvitation(u.ctx, &corev1.RespondToInvitationRequest{Account: "corp", Accept: true}); err != nil {
			t.Fatal(err)
		}
	}

	// Provision agents: 2 controlled by alice, 1 controlled by bobby, 1 unclaimed.
	aBot1, err := h.Auth.CreateDelegatedAgent(alice, &corev1.CreateDelegatedAgentRequest{Username: "alice-bot1"})
	if err != nil {
		t.Fatal(err)
	}
	aBot2, err := h.Auth.CreateDelegatedAgent(alice, &corev1.CreateDelegatedAgentRequest{Username: "alice-bot2"})
	if err != nil {
		t.Fatal(err)
	}
	bBot, err := h.Auth.CreateDelegatedAgent(bobby, &corev1.CreateDelegatedAgentRequest{Username: "bobby-bot"})
	if err != nil {
		t.Fatal(err)
	}
	unclaimed, err := mem.Auth.RegisterAgent(context.Background(), storage.RegisterAgentInput{
		Username: "unclaimed-bot", OwnerEmail: "nobody@example.com", DisplayName: "Unclaimed",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.Auth.CreateGroup(alice, &corev1.CreateGroupRequest{Name: "@corp/api-agents"}); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"alice-bot1", "alice-bot2", "bobby-bot", "unclaimed-bot"} {
		if _, err := h.Auth.AddGroupMember(alice, &corev1.AddGroupMemberRequest{Name: "@corp/api-agents", Username: member}); err != nil {
			t.Fatal(err)
		}
	}

	homeRef := &corev1.SliceRef{Account: "corp", Slice: "home"}
	for _, target := range []string{"alice-bot1", "alice-bot2", "bobby-bot", "unclaimed-bot"} {
		if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: homeRef, Username: target, Role: "writer"}); err != nil {
			t.Fatal(err)
		}
	}
	aBot1Ctx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID: aBot1.AgentSubjectId, SubjectKind: "agent", ControllerSubjectID: aliceID,
	})
	aBot2Ctx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID: aBot2.AgentSubjectId, SubjectKind: "agent", ControllerSubjectID: aliceID,
	})
	bBotCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID: bBot.AgentSubjectId, SubjectKind: "agent", ControllerSubjectID: bobbyID,
	})
	unclaimedCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID: unclaimed.SubjectID, SubjectKind: "agent", ControllerSubjectID: "",
	})

	headCommit := commitSliceFile(t, alice, h, mem, homeRef, "/corp/.gitslice/owners.yaml", `version: 1
owners:
  - paths: ["api/**"]
    owners: ["@corp/api-agents"]
    require: 2
  - paths: ["solo/**"]
    owners: ["alice"]
    require: 1
  - paths: ["pair/**"]
    owners: ["alice", "bobby"]
    require: 2
  - paths: ["unclaimed/**"]
    owners: ["unclaimed-bot"]
    require: 1
`)

	// Case A: @corp/api-agents (require: 2).
	// Unclaimed + 2 same-controller agents (alice-bot1 & alice-bot2) count as only 1!
	csAPI, psAPI := proposeEdits(t, carol, h, homeRef, headCommit, "api edit",
		upsertEdit(t, carol, h, homeRef, "/corp/api/a.go", "package api\n"),
	)
	for _, approver := range []context.Context{unclaimedCtx, aBot1Ctx, aBot2Ctx} {
		if _, err := h.Changeset.ApproveChangeset(approver, &corev1.ApproveChangesetRequest{ChangesetId: csAPI.Id}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csAPI.Id, ExpectedCurrentPatchsetId: psAPI.Id})
	wantCode(t, err, codes.FailedPrecondition, "2 same-controller agents + unclaimed < 2")
	if !strings.Contains(err.Error(), "has 1") {
		t.Fatalf("expected has 1, got: %v", err)
	}
	// Adding bobby-bot (distinct controller) brings distinct owner controller count to 2 -> succeeds!
	if _, err := h.Changeset.ApproveChangeset(bBotCtx, &corev1.ApproveChangesetRequest{ChangesetId: csAPI.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(carol, &corev1.SubmitChangesetRequest{ChangesetId: csAPI.Id, ExpectedCurrentPatchsetId: psAPI.Id}); err != nil {
		t.Fatalf("Submit after 2nd distinct group owner controller failed: %v", err)
	}

	// Case B: Self-ownership + co-author exclusion + SubmitMergeKind exclusion.
	// aliceBot1 alone owns solo/** (authorControllers={aliceID} ⊆ ownerControllers={aliceID}) -> 1 >= 1!
	csSoloBot, psSoloBot := proposeEdits(t, aBot1Ctx, h, homeRef, headCommit, "solo bot",
		upsertEdit(t, aBot1Ctx, h, homeRef, "/corp/solo/ok.go", "package solo\n"),
	)
	if _, err := h.Changeset.SubmitChangeset(aBot1Ctx, &corev1.SubmitChangesetRequest{ChangesetId: csSoloBot.Id, ExpectedCurrentPatchsetId: psSoloBot.Id}); err != nil {
		t.Fatalf("claimed agent of owner self-owning solo/**: %v", err)
	}

	// Non-owner collaborator carol adding a patchset invalidates self-ownership for alice!
	csSoloMix, psSolo1 := proposeEdits(t, alice, h, homeRef, headCommit, "solo mixed",
		upsertEdit(t, alice, h, homeRef, "/corp/solo/mix.go", "v1\n"),
	)
	if _, err := h.Changeset.AddChangesetCollaborator(alice, &corev1.AddChangesetCollaboratorRequest{ChangesetId: csSoloMix.Id, Username: "carol"}); err != nil {
		t.Fatal(err)
	}
	psSolo2, err := h.Changeset.UpdateChangeset(carol, &corev1.UpdateChangesetRequest{
		ChangesetId: csSoloMix.Id, ExpectedCurrentPatchsetId: psSolo1.Id, BaseCommitId: headCommit,
		FileEdits: []*corev1.FileEdit{upsertEdit(t, carol, h, homeRef, "/corp/solo/mix.go", "v2\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: csSoloMix.Id, ExpectedCurrentPatchsetId: psSolo2.Id})
	wantCode(t, err, codes.FailedPrecondition, "non-owner co-author invalidates self-ownership")

	// Mechanical SubmitMergeKind patchset added on top of alice's psMech1 preserves self-ownership!
	carolID, _ := authctx.SubjectID(carol)
	csMech, psMech1 := proposeEdits(t, alice, h, homeRef, headCommit, "solo mech",
		upsertEdit(t, alice, h, homeRef, "/corp/solo/mech.go", "v1\n"),
	)
	psMech2, err := mem.Changesets.AddPatchset(context.Background(), csMech.Id, psMech1.Id, &corev1.Patchset{
		BaseCommitId: headCommit,
		Author:       carolID,
		Kind:         storage.SubmitMergeKind,
		ChangedPaths: []string{"/corp/solo/mech.go"},
		FileEdits:    []*corev1.FileEdit{upsertEdit(t, alice, h, homeRef, "/corp/solo/mech.go", "v1\n")},
		PathBases:    psMech1.PathBases,
		ReadSet:      psMech1.ReadSet,
		WriteSet:     psMech1.WriteSet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: csMech.Id, ExpectedCurrentPatchsetId: psMech2.Id}); err != nil {
		t.Fatalf("mechanical merge patchset broke self-ownership: %v", err)
	}

	// Case C: pair/** require:2 -> alice + aliceBot1 approval = 1 (<2); alice + bobby = 2.
	csPair, psPair1 := proposeEdits(t, alice, h, homeRef, headCommit, "pair",
		upsertEdit(t, alice, h, homeRef, "/corp/pair/p.go", "v1\n"),
	)
	if _, err := h.Changeset.ApproveChangeset(aBot1Ctx, &corev1.ApproveChangesetRequest{ChangesetId: csPair.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: csPair.Id, ExpectedCurrentPatchsetId: psPair1.Id})
	wantCode(t, err, codes.FailedPrecondition, "same-controller bot approval under require:2")
	if _, err := h.Changeset.ApproveChangeset(bobby, &corev1.ApproveChangesetRequest{ChangesetId: csPair.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: csPair.Id, ExpectedCurrentPatchsetId: psPair1.Id}); err != nil {
		t.Fatalf("Submit pair/** with bobby approval failed: %v", err)
	}

	// Case D: Unclaimed owner fails closed even when self-authoring.
	csUncl, psUncl := proposeEdits(t, unclaimedCtx, h, homeRef, headCommit, "unclaimed",
		upsertEdit(t, unclaimedCtx, h, homeRef, "/corp/unclaimed/u.go", "x\n"),
	)
	_, err = h.Changeset.SubmitChangeset(unclaimedCtx, &corev1.SubmitChangesetRequest{ChangesetId: csUncl.Id, ExpectedCurrentPatchsetId: psUncl.Id})
	wantCode(t, err, codes.FailedPrecondition, "unclaimed owner authoring")

	// Case E: Implicit .gitslice/** rule blocks alice's slice-writer delegated agent (not account admin),
	// AND controller-dedups alice's approval on aliceBot1's .gitslice change, requiring admin2!
	csGS, psGS := proposeEdits(t, aBot1Ctx, h, homeRef, headCommit, "bot edits checks",
		upsertEdit(t, aBot1Ctx, h, homeRef, "/corp/.gitslice/checks.yaml", "version: 1\nchecks: {}\n"),
	)
	_, err = h.Changeset.SubmitChangeset(aBot1Ctx, &corev1.SubmitChangesetRequest{ChangesetId: csGS.Id, ExpectedCurrentPatchsetId: psGS.Id})
	wantCode(t, err, codes.FailedPrecondition, "writer delegated agent touching .gitslice/")
	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: csGS.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = h.Changeset.SubmitChangeset(aBot1Ctx, &corev1.SubmitChangesetRequest{ChangesetId: csGS.Id, ExpectedCurrentPatchsetId: psGS.Id})
	wantCode(t, err, codes.FailedPrecondition, "alice controller approving own bot .gitslice/ change")
	if _, err := h.Changeset.ApproveChangeset(admin2, &corev1.ApproveChangesetRequest{ChangesetId: csGS.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(aBot1Ctx, &corev1.SubmitChangesetRequest{ChangesetId: csGS.Id, ExpectedCurrentPatchsetId: psGS.Id}); err != nil {
		t.Fatalf("distinct account admin approving bot .gitslice/ change failed: %v", err)
	}
}

func TestPathOwnersEnforcedOnRenamesDeletesAndDirectPathOperations(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "user_bobby", "bobby")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "opsorg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "opsorg", Username: "bobby", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(bobby, &corev1.RespondToInvitationRequest{Account: "opsorg", Accept: true}); err != nil {
		t.Fatal(err)
	}

	appRef := &corev1.SliceRef{Account: "opsorg", Slice: "home"}
	data := []byte("v1\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_ops_seed", []storage.FileEntry{
		{Path: "/opsorg/app/api/secret.go", BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100644, Size: int64(len(data))},
		{Path: "/opsorg/app/public/readme.md", BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100644, Size: int64(len(data))},
		{Path: "/opsorg/app/folder/sub.go", BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100644, Size: int64(len(data))},
	}, []string{"/opsorg/app/api/secret.go"})

	headCommit := commitSliceFile(t, alice, h, mem, appRef, "/opsorg/app/.gitslice/owners.yaml",
		"version: 1\nowners:\n  - paths: [\"api/**\", \"dest/**\"]\n    owners: [\"bobby\"]\n    require: 1\n")

	// 1. Rename out of owned path (/opsorg/app/api/secret.go -> /opsorg/app/public/leaked.go) requires bobby.
	csRen, psRen := proposeEdits(t, alice, h, appRef, headCommit, "rename owned",
		&corev1.FileEdit{Op: "rename", OldPath: "/opsorg/app/api/secret.go", Path: "/opsorg/app/public/leaked.go"},
	)
	_, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: csRen.Id, ExpectedCurrentPatchsetId: psRen.Id})
	wantCode(t, err, codes.FailedPrecondition, "rename out of owned path without bobby")

	// 2. Directory rename (/opsorg/app/api -> /opsorg/app/public/api) and directory delete (/opsorg/app/api) require bobby.
	csDirRen, psDirRen := proposeEdits(t, alice, h, appRef, headCommit, "dir rename",
		&corev1.FileEdit{Op: "rename", OldPath: "/opsorg/app/api", Path: "/opsorg/app/public/api"},
	)
	_, err = h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: csDirRen.Id, ExpectedCurrentPatchsetId: psDirRen.Id})
	wantCode(t, err, codes.FailedPrecondition, "directory rename out of owned subtree")

	csDirDel, psDirDel := proposeEdits(t, alice, h, appRef, headCommit, "dir delete",
		&corev1.FileEdit{Op: "delete", Path: "/opsorg/app/api"},
	)
	_, err = h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: csDirDel.Id, ExpectedCurrentPatchsetId: psDirDel.Id})
	wantCode(t, err, codes.FailedPrecondition, "directory delete covering owned files")

	// 3. CopyPath and MovePath direct path operations cannot bypass .gitslice/owners.yaml.
	_, err = h.Changeset.CopyPath(alice, &corev1.CopyPathRequest{
		Slice: appRef, SourcePath: "/opsorg/app/public/readme.md", DestinationPath: "/opsorg/app/dest/readme.md",
	})
	wantCode(t, err, codes.FailedPrecondition, "CopyPath into owned path")

	_, err = h.Changeset.CopyPath(alice, &corev1.CopyPathRequest{
		Slice: appRef, SourcePath: "/opsorg/app/folder", DestinationPath: "/opsorg/app/dest/folder",
	})
	wantCode(t, err, codes.FailedPrecondition, "folder CopyPath into owned path")

	_, err = h.Changeset.MovePath(alice, &corev1.MovePathRequest{
		Slice: appRef, SourcePath: "/opsorg/app/api/secret.go", DestinationPath: "/opsorg/app/public/secret.go",
	})
	wantCode(t, err, codes.FailedPrecondition, "MovePath out of owned path")

	_, err = h.Changeset.MovePath(alice, &corev1.MovePathRequest{
		Slice: appRef, SourcePath: "/opsorg/app/api", DestinationPath: "/opsorg/app/public/api",
	})
	wantCode(t, err, codes.FailedPrecondition, "folder MovePath covering owned files")

	// Readable unowned MovePath inside slice by non-owner still succeeds cleanly.
	if _, err := h.Changeset.MovePath(alice, &corev1.MovePathRequest{
		Slice: appRef, SourcePath: "/opsorg/app/public/readme.md", DestinationPath: "/opsorg/app/public/readme2.md",
	}); err != nil {
		t.Fatalf("unowned MovePath failed: %v", err)
	}
}

func TestPathOwnersRequireHumanEnforcedAtSubmitAndStatus(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "user_bobby", "bobby")
	aliceID, _ := authctx.SubjectID(alice)

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "finops"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "finops", Username: "bobby", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(bobby, &corev1.RespondToInvitationRequest{Account: "finops", Accept: true}); err != nil {
		t.Fatal(err)
	}

	// Claimed agent controlled by alice (the owner).
	aliceBot, err := h.Auth.CreateDelegatedAgent(alice, &corev1.CreateDelegatedAgentRequest{Username: "alice-finbot"})
	if err != nil {
		t.Fatal(err)
	}
	homeRef := &corev1.SliceRef{Account: "finops", Slice: "home"}
	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: homeRef, Username: "alice-finbot", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	aliceBotCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID: aliceBot.AgentSubjectId, SubjectKind: "agent", ControllerSubjectID: aliceID,
	})

	headCommit := commitSliceFile(t, alice, h, mem, homeRef, "/finops/.gitslice/owners.yaml", `version: 1
owners:
  - paths: ["billing/**"]
    owners: ["alice"]
    require: 1
    require_human: 1
`)

	// 1. Claimed agent controlled by owner alice satisfies require:1 (controller match)
	//    but MUST NOT satisfy require_human:1 without a human owner approval!
	csBot, psBot := proposeEdits(t, aliceBotCtx, h, homeRef, headCommit, "bot billing change",
		upsertEdit(t, aliceBotCtx, h, homeRef, "/finops/billing/rate.go", "package billing\n"),
	)
	gotCS, err := h.Changeset.GetChangeset(alice, &corev1.GetChangesetRequest{ChangesetId: csBot.Id})
	if err != nil {
		t.Fatal(err)
	}
	if gotCS.GetApprovalStatus().GetSatisfied() {
		t.Fatalf("expected approvalStatus unsatisfied for agent author under require_human:1, got %+v", gotCS.GetApprovalStatus())
	}
	foundHumanReq := false
	for _, r := range gotCS.GetApprovalStatus().GetRequirements() {
		if r.GetKind() == "path_owners_human" {
			foundHumanReq = true
			if r.GetSatisfied() || r.GetRequired() != 1 || r.GetCurrent() != 0 {
				t.Fatalf("unexpected path_owners_human requirement: %+v", r)
			}
		}
	}
	if !foundHumanReq {
		t.Fatalf("expected path_owners_human requirement in %+v", gotCS.GetApprovalStatus().GetRequirements())
	}

	_, err = h.Changeset.SubmitChangeset(aliceBotCtx, &corev1.SubmitChangesetRequest{
		ChangesetId: csBot.Id, ExpectedCurrentPatchsetId: psBot.Id,
	})
	wantCode(t, err, codes.FailedPrecondition, "agent author cannot self-satisfy require_human")
	if !strings.Contains(err.Error(), "human approval(s)") {
		t.Fatalf("expected human approval block reason, got: %v", err)
	}

	// Non-owner human bobby approving does not satisfy owner require_human.
	if _, err := h.Changeset.ApproveChangeset(bobby, &corev1.ApproveChangesetRequest{ChangesetId: csBot.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = h.Changeset.SubmitChangeset(aliceBotCtx, &corev1.SubmitChangesetRequest{
		ChangesetId: csBot.Id, ExpectedCurrentPatchsetId: psBot.Id,
	})
	wantCode(t, err, codes.FailedPrecondition, "non-owner human cannot satisfy owner require_human")

	// Human owner alice approving satisfies require_human:1 -> submit succeeds!
	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: csBot.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(aliceBotCtx, &corev1.SubmitChangesetRequest{
		ChangesetId: csBot.Id, ExpectedCurrentPatchsetId: psBot.Id,
	}); err != nil {
		t.Fatalf("submit after human owner approved failed: %v", err)
	}

	// 2. Human owner alice authoring directly self-satisfies require_human:1.
	ref, err := h.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	csHuman, psHuman := proposeEdits(t, alice, h, homeRef, ref.CommitId, "human owner billing change",
		upsertEdit(t, alice, h, homeRef, "/finops/billing/invoice.go", "package billing\n"),
	)
	if _, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{
		ChangesetId: csHuman.Id, ExpectedCurrentPatchsetId: psHuman.Id,
	}); err != nil {
		t.Fatalf("human owner authoring directly should self-satisfy require_human:1, got: %v", err)
	}
}
