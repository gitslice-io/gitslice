package storage

import (
	"testing"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestChangesetHandlesAreShellSafe(t *testing.T) {
	ref := &corev1.SliceRef{Account: "alice", Slice: "first-one"}
	if got := ChangesetHandle(ref, 4); got != "alice:first-one@4" {
		t.Fatalf("ChangesetHandle() = %q", got)
	}
	if got := PatchsetHandle(ref, 4, 2); got != "alice:first-one@4.2" {
		t.Fatalf("PatchsetHandle() = %q", got)
	}
}

func TestParseChangesetHandle(t *testing.T) {
	// Canonical account:slice plus legacy account/slice for back-compat.
	for _, selector := range []string{
		"alice:first-one@4",
		"alice:first-one@4.2",
		"alice:first-one!4",
		"alice:first-one!4@2",
		"alice/first-one@4",
		"alice/first-one@4.2",
		"alice/first-one!4",
		"alice/first-one!4@2",
	} {
		account, slice, number, ok := ParseChangesetHandle(selector)
		if !ok {
			t.Fatalf("ParseChangesetHandle(%q) did not parse", selector)
		}
		if account != "alice" || slice != "first-one" || number != 4 {
			t.Fatalf("ParseChangesetHandle(%q) = %q %q %d", selector, account, slice, number)
		}
	}
}

func TestParsePatchsetHandle(t *testing.T) {
	for _, selector := range []string{
		"alice:first-one@4.2",
		"alice:first-one!4@2",
		"alice/first-one@4.2",
		"alice/first-one!4@2",
	} {
		account, slice, changesetNumber, patchsetNumber, ok := ParsePatchsetHandle(selector)
		if !ok {
			t.Fatalf("ParsePatchsetHandle(%q) did not parse", selector)
		}
		if account != "alice" || slice != "first-one" || changesetNumber != 4 || patchsetNumber != 2 {
			t.Fatalf("ParsePatchsetHandle(%q) = %q %q %d %d", selector, account, slice, changesetNumber, patchsetNumber)
		}
	}
}
