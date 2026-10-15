package cli

import (
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestApplyFileEditsToSnapshotReplaysStackAncestorEdits(t *testing.T) {
	ws := WorkspaceConfig{
		Account:       "acme",
		Slice:         "payment",
		IncludedPaths: []string{"/acme/payment"},
	}
	base := BaseSnapshot{Files: map[string]BaseSnapshotFile{
		"/acme/payment/a.go":       {Path: "/acme/payment/a.go", RelPath: "a.go", ContentHash: "sha256:a", Mode: 0o100644},
		"/acme/payment/dir/old.go": {Path: "/acme/payment/dir/old.go", RelPath: "dir/old.go", ContentHash: "sha256:old", Mode: 0o100644},
	}}
	edits := []*corev1.FileEdit{
		{Op: "rename", OldPath: "/acme/payment/dir", Path: "/acme/payment/renamed"},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: "sha256:b"},
		{Op: "delete", Path: "/acme/payment/a.go"},
	}

	if err := applyFileEditsToSnapshot(ws, &base, edits); err != nil {
		t.Fatal(err)
	}
	if _, ok := base.Files["/acme/payment/a.go"]; ok {
		t.Fatalf("deleted file remained in snapshot: %#v", base.Files)
	}
	renamed, ok := base.Files["/acme/payment/renamed/old.go"]
	if !ok {
		t.Fatalf("renamed file missing from snapshot: %#v", base.Files)
	}
	if renamed.RelPath != "acme/payment/renamed/old.go" {
		t.Fatalf("renamed rel path = %q", renamed.RelPath)
	}
	added, ok := base.Files["/acme/payment/b.go"]
	if !ok {
		t.Fatalf("added file missing from snapshot: %#v", base.Files)
	}
	if added.Mode != 0o100644 || added.RelPath != "acme/payment/b.go" {
		t.Fatalf("added snapshot file = %#v", added)
	}
}
