package postgres

import (
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Listings carry each slice's creation time and when a changeset authored
// from it last landed, for account pages to sort by.
func TestListSlicesActivity(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	listed := func() (created, active time.Time) {
		t.Helper()
		slices, err := store.Slices().List(ctx, "acme", "", 50, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, slice := range slices {
			if slice.GetRef().GetSlice() == "payment" {
				created, _ = time.Parse(time.RFC3339Nano, slice.CreatedAt)
				active, _ = time.Parse(time.RFC3339Nano, slice.LastActivityAt)
				return created, active
			}
		}
		t.Fatal("acme/payment is not listed")
		return
	}
	created, active := listed()
	if created.IsZero() || !active.Equal(created) {
		t.Fatalf("before a landing: created %v, active %v", created, active)
	}

	base := getTestRef(t, ctx, store)
	blobID, hash := upsertTestBlob(t, ctx, store, "package payment\nconst Active = true\n")
	ps := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/active.go", blobID, hash)
	if _, err := store.Changesets().Submit(ctx, ps.ChangesetId, ps.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, active = listed(); !active.After(created) {
		t.Fatalf("after a landing: created %v, active %v", created, active)
	}

	// Server-side active & newest keyset pagination order payment first, and put home ahead of all others when present.
	if _, err := store.Slices().Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "home"}, []string{"/acme"}, "private", 0, nil); err != nil {
		t.Fatalf("Create(acme/home): %v", err)
	}
	// Even if payment has newer activity or we sort by active, home comes first on page 1, then payment on page 2.
	blobID2, hash2 := upsertTestBlob(t, ctx, store, "package payment\nconst Active2 = true\n")
	base2 := getTestRef(t, ctx, store)
	ps2 := createDraftPatchset(t, ctx, store, base2.CommitId, "/acme/payment/active2.go", blobID2, hash2)
	if _, err := store.Changesets().Submit(ctx, ps2.ChangesetId, ps2.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}

	page1, nextCursor, err := store.Slices().ListSorted(ctx, "acme", "all", "", "", 1, false, "active")
	if err != nil {
		t.Fatalf("ListSorted(active) page 1: %v", err)
	}
	if len(page1) != 1 || page1[0].GetRef().GetSlice() != "home" || nextCursor == "" {
		t.Fatalf("ListSorted(active) page 1 = (%+v, %q), want home + nextCursor", page1, nextCursor)
	}
	page2, _, err := store.Slices().ListSorted(ctx, "acme", "all", "", nextCursor, 1, false, "active")
	if err != nil {
		t.Fatalf("ListSorted(active) page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].GetRef().GetSlice() != "payment" {
		t.Fatalf("ListSorted(active) page 2 = %+v, want payment (most recently active)", page2)
	}
}
