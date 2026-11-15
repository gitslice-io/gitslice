package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestMemoryChangesetSecurityInvariants(t *testing.T) {
	ctx := context.Background()
	stores := New()
	stores.AddPersonalAccount("user_alice", "alice")
	stores.AddPersonalAccount("user_bob", "bob")
	stores.AddPersonalAccount("user_carol", "carol")

	data := []byte("hello\n")
	blobID := objectid.BlobID(data)
	hash := objectid.RawContentHash(data)
	if err := stores.Blobs.Upsert(ctx, blobID, hash, int64(len(data)), "blobs/"+hash); err != nil {
		t.Fatal(err)
	}
	stores.PutCommitWithFiles("commit_base", nil, nil)
	slice, err := stores.Slices.Create(ctx, "user_alice", &corev1.SliceRef{Account: "alice", Slice: "guarded"}, []string{"/alice/guarded"}, "private", 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	cs, err := stores.Changesets.Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.Ref,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   "commit_base",
		Title:          "change",
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := stores.Changesets.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: "commit_base",
		Author:       "user_bob",
		ChangedPaths: []string{"/alice/guarded/a.txt"},
		FileEdits: []*corev1.FileEdit{
			{Op: "upsert", Path: "/alice/guarded/a.txt", BlobId: blobID, ContentHash: hash, Mode: 0o100644},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Patchset author cannot self-approve.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, "user_bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Submit with patchset author self-approval: err = %v, want ErrConflict", err)
	}

	// Third-party approval satisfies requirements, except when slice is archived.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, "user_carol"); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Slices.SetArchived(ctx, slice.Id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); !errors.Is(err, storage.ErrConflict) || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("Submit on archived slice: err = %v, want ErrConflict mentioning archived", err)
	}
	if _, err := stores.Slices.SetArchived(ctx, slice.Id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); err != nil {
		t.Fatalf("Submit on active slice: %v", err)
	}

	// Mutations while pending_publish are rejected with ErrConflict.
	nextTitle := "mutated"
	if _, err := stores.Changesets.UpdateInfo(ctx, cs.Id, &nextTitle, nil); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("UpdateInfo while pending_publish: err = %v, want ErrConflict", err)
	}
	if err := stores.Changesets.Abandon(ctx, cs.Id); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Abandon while pending_publish: err = %v, want ErrConflict", err)
	}
	if _, err := stores.Changesets.Approve(ctx, cs.Id, "user_carol"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Approve while pending_publish: err = %v, want ErrConflict", err)
	}
	if _, err := stores.Changesets.ReportCheckResult(ctx, cs.Id, "user_carol", "ci", "pass"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("ReportCheckResult while pending_publish: err = %v, want ErrConflict", err)
	}
	if _, err := stores.Changesets.AddPatchset(ctx, cs.Id, ps.Id, &corev1.Patchset{
		BaseCommitId: "commit_base",
		Author:       "user_alice",
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("AddPatchset while pending_publish: err = %v, want ErrConflict", err)
	}
}
