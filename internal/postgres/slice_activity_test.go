package postgres

import (
	"testing"
	"time"
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
}
