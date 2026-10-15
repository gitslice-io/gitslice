package postgres

import (
	"errors"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestStoragePathHeadRejectsSamePathBeforePublish(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	path := "/acme/payment/conflict.go"
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst V = 1\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst V = 2\n")

	first := createDraftPatchset(t, ctx, store, base.CommitId, path, firstBlobID, firstHash)
	second := createDraftPatchset(t, ctx, store, base.CommitId, path, secondBlobID, secondHash)

	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); !errors.Is(err, ErrConflict) {
		t.Fatalf("second submit err = %v, want ErrConflict", err)
	}
	ref := getTestRef(t, ctx, store)
	if ref.CommitId != base.CommitId {
		t.Fatal("ref moved before pending publish was processed")
	}
}

func TestStorageDisjointPendingPublishesAsBatch(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst B = 1\n")

	first := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/a.go", firstBlobID, firstHash)
	second := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/b.go", secondBlobID, secondHash)
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); err != nil {
		t.Fatal(err)
	}
	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 2 {
		t.Fatalf("published = %d, want 2", published)
	}
	ref := getTestRef(t, ctx, store)
	for _, path := range []string{"/acme/payment/a.go", "/acme/payment/b.go"} {
		if _, err := store.Repository().GetFile(ctx, ref.CommitId, path); err != nil {
			t.Fatalf("GetFile(%s): %v", path, err)
		}
	}
}

func TestStorageRefreshesDirectoryPathHeadsAfterPublish(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	first := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/src"},
		{Op: "upsert", Path: "/acme/payment/src/a.go", BlobId: firstBlobID, ContentHash: firstHash, Mode: 0o100644},
	})
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst B = 1\n")
	second := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/src"},
		{Op: "upsert", Path: "/acme/payment/src/b.go", BlobId: secondBlobID, ContentHash: secondHash, Mode: 0o100644},
	})
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); err != nil {
		t.Fatalf("second submit should accept existing mkdir after publish: %v", err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	ref := getTestRef(t, ctx, store)
	for _, path := range []string{"/acme/payment/src/a.go", "/acme/payment/src/b.go"} {
		if _, err := store.Repository().GetFile(ctx, ref.CommitId, path); err != nil {
			t.Fatalf("GetFile(%s): %v", path, err)
		}
	}
}

func TestStorageRefreshesRenamedDirectoryDescendantPathHeads(t *testing.T) {
	ctx, store, objectStore := newPostgresTestStoreWithObjects(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst A = 1\n")
	secondBlobID, secondHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst B = 1\n")
	seed := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/src"},
		{Op: "mkdir", Path: "/acme/payment/src/nested"},
		{Op: "upsert", Path: "/acme/payment/src/a.go", BlobId: firstBlobID, ContentHash: firstHash, Mode: 0o100644},
		{Op: "upsert", Path: "/acme/payment/src/nested/b.go", BlobId: secondBlobID, ContentHash: secondHash, Mode: 0o100644},
	})
	if _, err := store.Changesets().Submit(ctx, seed.ChangesetId, seed.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	move := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:      "rename",
		OldPath: "/acme/payment/src",
		Path:    "/acme/payment/moved",
	}})
	if _, err := store.Changesets().Submit(ctx, move.ChangesetId, move.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	ref := getTestRef(t, ctx, store)
	if _, err := store.Repository().GetFile(ctx, ref.CommitId, "/acme/payment/moved/nested/b.go"); err != nil {
		t.Fatalf("moved file not readable: %v", err)
	}
	rootTreeID, err := store.Repository().RootTreeForCommit(ctx, ref.CommitId)
	if err != nil {
		t.Fatal(err)
	}
	wantHeads := map[string]PathHead{}
	for _, p := range []string{
		"/acme",
		"/acme/payment",
		"/acme/payment/moved",
		"/acme/payment/moved/a.go",
		"/acme/payment/moved/nested",
		"/acme/payment/moved/nested/b.go",
	} {
		entry, err := store.Repository().GetEntryAtTree(ctx, rootTreeID, p)
		if err != nil {
			t.Fatal(err)
		}
		wantHeads[p] = pathHeadFromTreeEntry(*entry)
	}
	for _, p := range []string{
		"/acme/payment/src",
		"/acme/payment/src/a.go",
		"/acme/payment/src/nested",
		"/acme/payment/src/nested/b.go",
	} {
		wantHeads[p] = PathHead{
			Path:             p,
			EntryFingerprint: MissingEntryFingerprint(),
		}
	}
	assertPathHeadRowsForTest(t, ctx, store, wantHeads, move.ChangesetId, move.Id)

	report, err := store.VerifyIntegrity(ctx, objectStore)
	if err != nil || !report.OK() {
		t.Fatalf("integrity after directory rename failed: err=%v report=%#v", err, report)
	}
}
