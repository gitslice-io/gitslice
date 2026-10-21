package service

import (
	"errors"
	"fmt"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestAcceptedExpectedPatchsetMeansTheServersMergeOfIt(t *testing.T) {
	merged := &corev1.Changeset{
		CurrentPatchsetId: "ps2",
		Patchsets:         []*corev1.Patchset{{Id: "ps1"}, {Id: "ps2", Kind: storage.SubmitMergeKind}},
	}
	if got := acceptedExpectedPatchset(merged, "ps1"); got != "ps2" {
		t.Fatalf("expected ps1 on a server merge of it = %q, want ps2", got)
	}
	for _, expected := range []string{"", "ps2", "ps0"} {
		if got := acceptedExpectedPatchset(merged, expected); got != expected {
			t.Fatalf("expected %q = %q, want it unchanged", expected, got)
		}
	}
	// A patchset the author made is not taken for one they did not see.
	authored := &corev1.Changeset{
		CurrentPatchsetId: "ps2",
		Patchsets:         []*corev1.Patchset{{Id: "ps1"}, {Id: "ps2"}},
	}
	if got := acceptedExpectedPatchset(authored, "ps1"); got != "ps1" {
		t.Fatalf("expected ps1 on an author's ps2 = %q, want ps1 (a conflict)", got)
	}
}

func TestIsStaleBaseOnlyMatchesTheStaleBaseRefusal(t *testing.T) {
	if !isStaleBase(fmt.Errorf("%w: %s", storage.ErrConflict, storage.StaleBaseReason)) {
		t.Fatal("the stale-base refusal is not recognized")
	}
	for _, err := range []error{nil, errors.New(storage.StaleBaseReason), fmt.Errorf("%w: required approvals not satisfied", storage.ErrConflict)} {
		if isStaleBase(err) {
			t.Fatalf("isStaleBase(%v) = true", err)
		}
	}
}

func TestConflictPathListShowsFiveAndCountsTheRest(t *testing.T) {
	var conflicts []*corev1.PatchsetConflict
	for i := 0; i < 7; i++ {
		conflicts = append(conflicts, &corev1.PatchsetConflict{Path: fmt.Sprintf("/a/%d", i)})
	}
	if got := conflictPathList(conflicts); got != "/a/0, /a/1, /a/2, /a/3, /a/4 and 2 more" {
		t.Fatalf("conflictPathList = %q", got)
	}
}
