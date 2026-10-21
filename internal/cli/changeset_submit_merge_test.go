package cli

import (
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestLandedByServerMerge(t *testing.T) {
	merged := &corev1.Changeset{CurrentPatchsetId: "ps2", Patchsets: []*corev1.Patchset{{Id: "ps1"}, {Id: "ps2", Kind: storage.SubmitMergeKind}}}
	if !landedByServerMerge(merged, "ps1") {
		t.Fatal("a server merge of the submitted patchset was not noticed")
	}
	if landedByServerMerge(merged, "ps2") {
		t.Fatal("landing the submitted patchset itself counted as a merge")
	}
	authored := &corev1.Changeset{CurrentPatchsetId: "ps2", Patchsets: []*corev1.Patchset{{Id: "ps1"}, {Id: "ps2"}}}
	if landedByServerMerge(authored, "ps1") {
		t.Fatal("a patchset the author made counted as a server merge")
	}
}
