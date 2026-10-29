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
