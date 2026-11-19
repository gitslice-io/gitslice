package service

import (
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestGetChangesetApprovalStatusSliceAndGitsliceRules(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := asUser(t, handlers, "user_alice", "alice")
	bob := asUser(t, handlers, "user_bob", "bobby")
	carol := asUser(t, handlers, "user_carol", "carol")

	if _, err := handlers.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "labs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{
		Account: "labs", Username: "bobby", Role: "writer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.RespondToInvitation(bob, &corev1.RespondToInvitationRequest{
		Account: "labs", Accept: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{
		Account: "labs", Username: "carol", Role: "writer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.RespondToInvitation(carol, &corev1.RespondToInvitationRequest{
		Account: "labs", Accept: true,
	}); err != nil {
		t.Fatal(err)
	}

	seedPathInMemoryRepo(t, mem, "commit_base", "/labs/app/main.go", "package main\n")

	if _, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:                    &corev1.SliceRef{Account: "labs", Slice: "app"},
		IncludedPaths:          []string{"/labs/app"},
		Visibility:             "private",
		RequiredApprovals:      1,
		RequiredHumanApprovals: 1,
	}); err != nil {
		t.Fatal(err)
	}

	cs, err := handlers.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "labs", Slice: "app"},
		Title:          "update main",
		BaseCommitId:   "commit_base",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("package main\n// updated\n")
	if _, err := handlers.Blob.UploadBlob(bob, &corev1.UploadBlobRequest{
		Slice: &corev1.SliceRef{Account: "labs", Slice: "app"},
		Data:  body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: "commit_base",
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/labs/app/main.go",
			BlobId:      objectid.BlobID(body),
			ContentHash: objectid.RawContentHash(body),
			Mode:        0o100644,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := handlers.Changeset.GetChangeset(bob, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetApprovalStatus() == nil {
		t.Fatal("expected approval_status to be populated")
	}
	if got.GetApprovalStatus().GetSatisfied() {
		t.Fatalf("expected approval_status.satisfied=false before approvals, got %+v", got.GetApprovalStatus())
	}
	if len(got.GetApprovalStatus().GetRequirements()) != 2 {
		t.Fatalf("expected 2 approval requirements, got %+v", got.GetApprovalStatus().GetRequirements())
	}

	// Carol (distinct human writer) approves -> both slice_approvals and slice_human_approvals satisfied.
	if _, err := handlers.Changeset.ApproveChangeset(carol, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	got, err = handlers.Changeset.GetChangeset(bob, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetApprovalStatus().GetSatisfied() {
		t.Fatalf("expected approval_status.satisfied=true after carol approved, got %+v", got.GetApprovalStatus())
	}
	for _, req := range got.GetApprovalStatus().GetRequirements() {
		if !req.GetSatisfied() || req.GetCurrent() != 1 || req.GetRequired() != 1 {
			t.Fatalf("unexpected requirement state: %+v", req)
		}
	}

	// Now bob edits a .gitslice/ config file on a new changeset -> requires account owner/admin (alice).
	csCfg, err := handlers.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "labs", Slice: "app"},
		Title:          "edit checks",
		BaseCommitId:   "commit_base",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := []byte("version: 1\nchecks: {}\n")
	if _, err := handlers.Blob.UploadBlob(bob, &corev1.UploadBlobRequest{
		Slice: &corev1.SliceRef{Account: "labs", Slice: "app"},
		Data:  cfg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  csCfg.Id,
		BaseCommitId: "commit_base",
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/labs/app/.gitslice/checks.yaml",
			BlobId:      objectid.BlobID(cfg),
			ContentHash: objectid.RawContentHash(cfg),
			Mode:        0o100644,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	// Carol (non-owner writer) approves: satisfies slice approvals, NOT gitslice_config.
	if _, err := handlers.Changeset.ApproveChangeset(carol, &corev1.ApproveChangesetRequest{ChangesetId: csCfg.Id}); err != nil {
		t.Fatal(err)
	}
	gotCfg, err := handlers.Changeset.GetChangeset(bob, &corev1.GetChangesetRequest{ChangesetId: csCfg.Id})
	if err != nil {
		t.Fatal(err)
	}
	if gotCfg.GetApprovalStatus().GetSatisfied() {
		t.Fatalf("expected gitslice_config requirement to remain unsatisfied when only writer approved: %+v", gotCfg.GetApprovalStatus())
	}
	var sawCfg bool
	for _, req := range gotCfg.GetApprovalStatus().GetRequirements() {
		if req.GetKind() == "gitslice_config" {
			sawCfg = true
			if req.GetSatisfied() {
				t.Fatalf("expected gitslice_config satisfied=false, got %+v", req)
			}
		}
	}
	if !sawCfg {
		t.Fatalf("expected gitslice_config requirement in %+v", gotCfg.GetApprovalStatus().GetRequirements())
	}

	// Owner alice approves -> all requirements satisfied.
	if _, err := handlers.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: csCfg.Id}); err != nil {
		t.Fatal(err)
	}
	gotCfg, err = handlers.Changeset.GetChangeset(bob, &corev1.GetChangesetRequest{ChangesetId: csCfg.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !gotCfg.GetApprovalStatus().GetSatisfied() {
		t.Fatalf("expected all requirements satisfied after owner approval, got %+v", gotCfg.GetApprovalStatus())
	}
}
