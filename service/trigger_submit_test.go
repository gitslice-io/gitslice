package service

import (
	"strings"
	"testing"

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
	own, _ := createStandaloneChangesetWithPatchset(t, alice, h, ref.CommitId, "own", "/acme/payment/.gitslice/triggers.yaml", "version: 1\ntriggers: {}\n")
	if _, err := h.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: own.Id}); err != nil {
		t.Fatalf("an owner submitting a .gitslice change: %v", err)
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
