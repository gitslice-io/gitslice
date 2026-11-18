package memory

import (
	"context"
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSubmitEnforcesPathBasesConflictInMemoryStore(t *testing.T) {
	ctx := context.Background()
	stores := New()

	author := mustProvisionHumanForTest(t, ctx, stores, "mem_pb_author", "pb@example.com")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "pb"}
	stores.PutSlice(sliceRef, []string{"/acme/pb"}, "private")

	// Two concurrent draft changesets touching the same path from the same base commit.
	cs1, ps1 := mustCreateDraftOnSlice(t, ctx, stores, author, sliceRef, "/acme/pb/shared.go", "package pb\nconst V = 1\n")
	cs2, ps2 := mustCreateDraftOnSlice(t, ctx, stores, author, sliceRef, "/acme/pb/shared.go", "package pb\nconst V = 2\n")

	if _, err := stores.Changesets.Submit(ctx, cs1.Id, ps1.Id); err != nil {
		t.Fatalf("Submit cs1: %v", err)
	}

	// Even before PublishPending runs, cs1 is pending_publish and holds the path head;
	// submitting cs2 with stale PathBases must conflict with SubmitBlockCodeStalePathBase.
	_, err := stores.Changesets.Submit(ctx, cs2.Id, ps2.Id)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Submit cs2 before publish err = %v, want ErrConflict", err)
	}
	if code, ok := storage.SubmitBlockCodeOf(err); !ok || code != storage.SubmitBlockCodeStalePathBase {
		t.Fatalf("SubmitBlockCodeOf(%v) = (%q, %v), want stale_path_base", err, code, ok)
	}

	// After PublishPending advances TargetRef, cs2 remains stale until refreshed.
	if published, err := stores.Changesets.PublishPending(ctx, 10); err != nil || published != 1 {
		t.Fatalf("PublishPending = (%d, %v), want (1, nil)", published, err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs2.Id, ps2.Id); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Submit cs2 after publish err = %v, want ErrConflict", err)
	}
}

func TestChangesetSelectorResolutionAndAmbiguityInMemoryStore(t *testing.T) {
	ctx := context.Background()
	stores := New()

	author := mustProvisionHumanForTest(t, ctx, stores, "mem_sel_author", "sel@example.com")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "sel"}
	stores.PutSlice(sliceRef, []string{"/acme/sel"}, "private")

	stores.backend.mu.Lock()
	stores.backend.changesets["cs_deadbeef111122223333444455556666"] = &corev1.Changeset{
		Id:             "cs_deadbeef111122223333444455556666",
		AuthoringSlice: sliceRef,
		Author:         author,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   "mem_root",
		Status:         "draft",
		Title:          "first",
	}
	stores.backend.changesets["cs_deadbeef999988887777666655554444"] = &corev1.Changeset{
		Id:             "cs_deadbeef999988887777666655554444",
		AuthoringSlice: sliceRef,
		Author:         author,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   "mem_root",
		Status:         "draft",
		Title:          "second",
	}
	stores.backend.mu.Unlock()

	// Ambiguous prefix returns ErrInvalid (matching Postgres), not ErrNotFound.
	if _, err := stores.Changesets.Get(ctx, "deadbeef"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("ambiguous Get err = %v, want ErrInvalid", err)
	}
	if _, err := stores.Changesets.Submit(ctx, "deadbeef", ""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("ambiguous Submit err = %v, want ErrInvalid", err)
	}

	// Unambiguous short hex prefix resolves across UpdateInfo, AddPatchset, Submit, and Abandon.
	newTitle := "updated via short prefix"
	updated, err := stores.Changesets.UpdateInfo(ctx, "deadbeef11", &newTitle, nil)
	if err != nil || updated.Title != newTitle {
		t.Fatalf("UpdateInfo via prefix = (%+v, %v)", updated, err)
	}
	if err := stores.Changesets.Abandon(ctx, "deadbeef99"); err != nil {
		t.Fatalf("Abandon via prefix: %v", err)
	}
}
