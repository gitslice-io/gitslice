package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

// A change under .gitslice decides what code runs with which secrets, so an
// account writer needs an owner's or admin's approval to land one
// (design/34_triggers.md); other changes need none.
func TestGitsliceChangesNeedAnAdminApproval(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_bob", "acme", "writer")
	alice, bob := subjectCtx("user_alice"), subjectCtx("user_bob")
	ref, err := h.Repository.GetRef(bob, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}

	plain, _ := createStandaloneChangesetWithPatchset(t, bob, h, ref.CommitId, "plain", "/acme/payment/main.go", "package main\n")
	if _, err := h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{ChangesetId: plain.Id}); err != nil {
		t.Fatalf("submit a plain change: %v", err)
	}

	ref, _ = h.Repository.GetRef(bob, &corev1.GetRefRequest{})
	cs, _ := createStandaloneChangesetWithPatchset(t, bob, h, ref.CommitId, "checks", "/acme/payment/.gitslice/checks.yaml", "version: 1\nchecks: {}\n")
	_, err = h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id})
	wantCode(t, err, codes.FailedPrecondition, "a writer submitting a .gitslice change")
	if !strings.Contains(err.Error(), "an owner or admin of acme must approve") {
		t.Fatalf("error = %v", err)
	}
	// The author's own approval does not count; an owner's does.
	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatalf("submit after an owner approved: %v", err)
	}

	// An owner lands one alone.
	ref, _ = h.Repository.GetRef(alice, &corev1.GetRefRequest{})
	own, _ := createStandaloneChangesetWithPatchset(t, alice, h, ref.CommitId, "own", "/acme/payment/.gitslice/automations.yaml", "version: 1\nautomations: {}\n")
	if _, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: own.Id}); err != nil {
		t.Fatalf("an owner submitting a .gitslice change: %v", err)
	}

	// A claimed non-admin agent controlled by the human owner is blocked on
	// .gitslice/ edits until its human owner explicitly approves; once approved,
	// both GetChangeset approval_status and SubmitChangeset succeed.
	agentResp, err := h.Auth.CreateDelegatedAgent(alice, &corev1.CreateDelegatedAgentRequest{Username: "alice-bot"})
	if err != nil {
		t.Fatal(err)
	}
	mem.AddAccountRole(agentResp.AgentSubjectId, "acme", "writer")
	agentCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           agentResp.AgentSubjectId,
		SubjectKind:         "agent",
		ControllerSubjectID: "user_alice",
	})
	ref, _ = h.Repository.GetRef(agentCtx, &corev1.GetRefRequest{})
	agentCS, _ := createStandaloneChangesetWithPatchset(t, agentCtx, h, ref.CommitId, "agent config", "/acme/payment/.gitslice/agent_checks.yaml", "version: 1\nchecks: {}\n")
	gotBefore, err := h.Changeset.GetChangeset(alice, &corev1.GetChangesetRequest{ChangesetId: agentCS.Id})
	if err != nil {
		t.Fatal(err)
	}
	if gotBefore.GetApprovalStatus().GetSatisfied() {
		t.Fatalf("expected agent .gitslice changeset to need approval before human owner approves, got %+v", gotBefore.GetApprovalStatus())
	}
	_, err = h.Changeset.SubmitChangeset(agentCtx, &corev1.SubmitChangesetRequest{ChangesetId: agentCS.Id})
	wantCode(t, err, codes.FailedPrecondition, "non-admin claimed agent submitting .gitslice change")

	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: agentCS.Id}); err != nil {
		t.Fatal(err)
	}
	gotAfter, err := h.Changeset.GetChangeset(alice, &corev1.GetChangesetRequest{ChangesetId: agentCS.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !gotAfter.GetApprovalStatus().GetSatisfied() {
		t.Fatalf("expected human owner approval to satisfy .gitslice requirement, got %+v", gotAfter.GetApprovalStatus())
	}
	if _, err := h.Changeset.SubmitChangeset(agentCtx, &corev1.SubmitChangesetRequest{ChangesetId: agentCS.Id}); err != nil {
		t.Fatalf("submit after human owner approved own agent .gitslice change: %v", err)
	}
}

func TestGitsliceApprovalChecksPatchsetAuthorAndCurrentPatchset(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_bob", "acme", "writer")
	alice, bob := subjectCtx("user_alice"), subjectCtx("user_bob")

	ref, err := h.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Admin creates a benign changeset, then non-admin writer pushes a hijacked patchset touching .gitslice/.
	cs, ps1 := createStandaloneChangesetWithPatchset(t, alice, h, ref.CommitId, "benign", "/acme/payment/benign.go", "package payment\n")
	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	// Writers act only on changesets they own (design/36 section 2): the admin makes bob a collaborator.
	if _, err := h.Changeset.AddChangesetCollaborator(alice, &corev1.AddChangesetCollaboratorRequest{ChangesetId: cs.Id, Username: "bob"}); err != nil {
		t.Fatal(err)
	}
	upload, err := h.Blob.UploadBlob(bob, &corev1.UploadBlobRequest{
		Data:  []byte("version: 1\nchecks:\n  pwn:\n    run: env\n"),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ps2, err := h.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: ps1.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/.gitslice/checks.yaml",
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Earlier admin approval on ps1 and admin cs.Author must NOT let bob's ps2 submit!
	_, err = h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: ps2.Id,
	})
	wantCode(t, err, codes.FailedPrecondition, "non-admin hijacking admin changeset with .gitslice edit")

	// Once admin explicitly approves ps2, submit succeeds.
	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: ps2.Id,
	}); err != nil {
		t.Fatalf("submit after admin approved current patchset: %v", err)
	}
}

func TestFolderRequiredChecksGateSubmitAndResistSpoofing(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_bob", "acme", "writer")
	alice, bob := subjectCtx("user_alice"), subjectCtx("user_bob")

	// Slice acme/payment has NO slice-level required_checks.
	// Configure a folder-level required check in /acme/payment/.gitslice/checks.yaml.
	ref, err := h.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cfgCS, _ := createStandaloneChangesetWithPatchset(t, alice, h, ref.CommitId, "add folder required check",
		"/acme/payment/.gitslice/checks.yaml",
		"version: 1\nchecks:\n  unit:\n    run: go test ./...\n    required: true\n    paths: [\"src/**\"]\n")
	if _, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: cfgCS.Id}); err != nil {
		t.Fatalf("land checks.yaml: %v", err)
	}
	if _, err := mem.Changesets.PublishPending(context.Background(), 16); err != nil {
		t.Fatalf("publish checks.yaml: %v", err)
	}

	// 1. Change outside paths: ["src/**"] is NOT blocked by payment/unit.
	ref, _ = h.Repository.GetRef(bob, &corev1.GetRefRequest{})
	docCS, _ := createStandaloneChangesetWithPatchset(t, bob, h, ref.CommitId, "docs", "/acme/payment/README.md", "hi\n")
	if _, err := h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{ChangesetId: docCS.Id}); err != nil {
		t.Fatalf("non-matching path should submit without payment/unit: %v", err)
	}

	// 2. Change inside /acme/payment/src/** IS blocked by payment/unit even with empty slice required_checks.
	ref, _ = h.Repository.GetRef(bob, &corev1.GetRefRequest{})
	codeCS, codePS := createStandaloneChangesetWithPatchset(t, bob, h, ref.CommitId, "code", "/acme/payment/src/pay.go", "package pay\n")
	_, err = h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{
		ChangesetId:               codeCS.Id,
		ExpectedCurrentPatchsetId: codePS.Id,
	})
	wantCode(t, err, codes.FailedPrecondition, "folder required check missing")
	if !strings.Contains(err.Error(), `required check "payment/unit" has no result`) {
		t.Fatalf("unexpected error: %v", err)
	}

	// 3. Anti-spoofing: a draft that edits checks.yaml to remove required:true STILL cannot bypass the landed gate.
	upCode, err := h.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{
		Data:  []byte("package pay // v2\n"),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	upCfg, err := h.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{
		Data:  []byte("version: 1\nchecks:\n  unit:\n    run: go test ./...\n    required: false\n    paths: [\"src/**\"]\n"),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spoofCS, err := h.Changeset.CreateChangeset(alice, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		BaseCommitId:   ref.CommitId,
		Title:          "try bypass",
	})
	if err != nil {
		t.Fatal(err)
	}
	spoofPS, err := h.Changeset.UpdateChangeset(alice, &corev1.UpdateChangesetRequest{
		ChangesetId:  spoofCS.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{
			{Op: "upsert", Path: "/acme/payment/src/pay.go", BlobId: upCode.BlobId, ContentHash: upCode.ContentHash, Mode: 0o100644},
			{Op: "upsert", Path: "/acme/payment/.gitslice/checks.yaml", BlobId: upCfg.BlobId, ContentHash: upCfg.ContentHash, Mode: 0o100644},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{
		ChangesetId:               spoofCS.Id,
		ExpectedCurrentPatchsetId: spoofPS.Id,
	})
	wantCode(t, err, codes.FailedPrecondition, "draft cannot disable landed required check for same change")
	if !strings.Contains(err.Error(), `required check "payment/unit"`) {
		t.Fatalf("unexpected spoof error: %v", err)
	}

	// 4. Once payment/unit passes, bob's code changeset submits cleanly.
	if _, err := h.Changeset.ReportCheckResult(bob, &corev1.ReportCheckResultRequest{
		ChangesetId: codeCS.Id,
		CheckName:   "payment/unit",
		Status:      "pass",
	}); err != nil {
		t.Fatalf("ReportCheckResult: %v", err)
	}
	if _, err := h.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{
		ChangesetId:               codeCS.Id,
		ExpectedCurrentPatchsetId: codePS.Id,
	}); err != nil {
		t.Fatalf("submit after folder required check passed: %v", err)
	}
}
