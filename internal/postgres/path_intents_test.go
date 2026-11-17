package postgres

import (
	"errors"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestMigration0059AndPathIntentsPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	var exists bool
	if err := store.db.QueryRowContext(ctx, `
		select exists (
			select 1 from information_schema.tables
			where table_schema = current_schema() and table_name = 'path_intents'
		)
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("missing table path_intents after migration 0059")
	}

	insertPGPersonalUser(t, ctx, store, "user_alice", "alice", "Alice")
	insertPGPersonalUser(t, ctx, store, "user_bob", "bob", "Bob")

	csStore := store.Changesets()
	slices := store.Slices()
	slice, err := slices.Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: "payment"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	active1, err := csStore.CreatePathIntent(ctx, storage.PathIntent{
		SliceID:   slice.GetId(),
		Account:   "acme",
		Slice:     "payment",
		SubjectID: "user_alice",
		Pattern:   "/acme/payment/billing/**",
		Message:   "invoice v2",
		ExpiresAt: now.Add(2 * time.Hour),
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreatePathIntent active1: %v", err)
	}
	if active1.ID == "" || active1.ChangesetID != "" {
		t.Fatalf("unexpected active1: %+v", active1)
	}

	// Expired intent must not appear in ListPathIntents.
	if _, err := csStore.CreatePathIntent(ctx, storage.PathIntent{
		SliceID:   slice.GetId(),
		Account:   "acme",
		Slice:     "payment",
		SubjectID: "user_alice",
		Pattern:   "/acme/payment/old/**",
		ExpiresAt: now.Add(-time.Minute),
		CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	bobIntent, err := csStore.CreatePathIntent(ctx, storage.PathIntent{
		SliceID:   slice.GetId(),
		Account:   "acme",
		Slice:     "payment",
		SubjectID: "user_bob",
		Pattern:   "/acme/payment/billing/**",
		Message:   "bob separate reservation",
		ExpiresAt: now.Add(time.Hour),
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := csStore.ListPathIntents(ctx, slice.GetId(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListPathIntents = %d, want 2: %+v", len(list), list)
	}

	// LinkPathIntentsToChangeset enforces subject & slice ownership; bob cannot link alice's intent.
	ref, err := store.Repository().GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	csAbandon, err := csStore.Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.GetRef(),
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.GetCommitId(),
		Title:          "Abandon test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := csStore.LinkPathIntentsToChangeset(ctx, []string{active1.ID}, "user_bob", slice.GetId(), csAbandon.GetId()); err != nil {
		t.Fatal(err)
	}
	gotAfterWrongLink, err := csStore.GetPathIntent(ctx, active1.ID)
	if err != nil || gotAfterWrongLink.ChangesetID != "" {
		t.Fatalf("wrong subject linked intent: %+v, err=%v", gotAfterWrongLink, err)
	}

	// Create a dedicated intent linked to csAbandon and verify Abandon deletes it.
	abandonIntent, err := csStore.CreatePathIntent(ctx, storage.PathIntent{
		SliceID:     slice.GetId(),
		Account:     "acme",
		Slice:       "payment",
		SubjectID:   "user_alice",
		Pattern:     "/acme/payment/tmp/**",
		ChangesetID: csAbandon.GetId(),
		ExpiresAt:   now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := csStore.Abandon(ctx, csAbandon.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := csStore.GetPathIntent(ctx, abandonIntent.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected abandonIntent deleted on Abandon, got err=%v", err)
	}

	// Land a changeset by alice touching /acme/payment/billing/invoice.go:
	// active1 overlaps and clears inside PublishPending, while bobIntent remains intact!
	csLand, err := csStore.Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.GetRef(),
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.GetCommitId(),
		Title:          "Land invoice v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("package billing\nconst V = 2\n")
	hash := objectid.RawContentHash(data)
	blobID := objectid.BlobID(data)
	if err := store.Blobs().Upsert(ctx, blobID, hash, int64(len(data)), "test://"+hash); err != nil {
		t.Fatal(err)
	}
	baseTreeID, err := store.Repository().RootTreeForCommit(ctx, ref.GetCommitId())
	if err != nil {
		t.Fatal(err)
	}
	ps, err := csStore.AddPatchset(ctx, csLand.GetId(), "", &corev1.Patchset{
		BaseCommitId: ref.GetCommitId(),
		BaseKind:     "commit",
		BaseTreeId:   baseTreeID,
		Author:       "user_alice",
		ChangedPaths: []string{"/acme/payment/billing/invoice.go"},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/billing/invoice.go",
			BlobId:      blobID,
			ContentHash: hash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := csStore.Submit(ctx, csLand.GetId(), ps.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := csStore.PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}

	if _, err := csStore.GetPathIntent(ctx, active1.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected active1 cleared on PublishPending, got err=%v", err)
	}
	if _, err := csStore.GetPathIntent(ctx, bobIntent.ID); err != nil {
		t.Fatalf("expected bobIntent untouched by alice's land, got err=%v", err)
	}

	if err := csStore.DeletePathIntent(ctx, bobIntent.ID); err != nil {
		t.Fatalf("DeletePathIntent: %v", err)
	}
	if err := csStore.DeletePathIntent(ctx, bobIntent.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second DeletePathIntent = %v, want ErrNotFound", err)
	}
}
