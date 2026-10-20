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

func TestApplyFileEditsToSnapshotCopiesKeepTheSource(t *testing.T) {
	ws := WorkspaceConfig{Account: "acme", Slice: "payment", IncludedPaths: []string{"/acme/payment"}}
	base := BaseSnapshot{Files: map[string]BaseSnapshotFile{
		"/acme/payment/lib/a.go":     {Path: "/acme/payment/lib/a.go", RelPath: "acme/payment/lib/a.go", ContentHash: "sha256:a", Mode: 0o100644},
		"/acme/payment/lib/sub/b.sh": {Path: "/acme/payment/lib/sub/b.sh", RelPath: "acme/payment/lib/sub/b.sh", ContentHash: "sha256:b", Mode: 0o100755},
		"/acme/payment/library.go":   {Path: "/acme/payment/library.go", RelPath: "acme/payment/library.go", ContentHash: "sha256:l", Mode: 0o100644},
	}}
	edits := []*corev1.FileEdit{{Op: "copy", OldPath: "/acme/payment/lib", Path: "/acme/payment/vendor/lib"}}

	if err := applyFileEditsToSnapshot(ws, &base, edits); err != nil {
		t.Fatal(err)
	}
	if len(base.Files) != 5 {
		t.Fatalf("snapshot has %d files, want the 3 originals and 2 copies: %#v", len(base.Files), base.Files)
	}
	copied := base.Files["/acme/payment/vendor/lib/sub/b.sh"]
	if copied.ContentHash != "sha256:b" || copied.Mode != 0o100755 || copied.RelPath != "acme/payment/vendor/lib/sub/b.sh" {
		t.Fatalf("copied snapshot file = %#v", copied)
	}
	if _, ok := base.Files["/acme/payment/lib/a.go"]; !ok {
		t.Fatal("copy removed its source")
	}
}
