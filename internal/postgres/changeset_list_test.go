package postgres

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"gitslice.io/gitslice/proto/core/v1"
)

// A summary list is the full list without patchsets: same changesets, same
// order, same fields, including the current patchset's submit requirements.
func TestListChangesetSummaries(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, hash := upsertTestBlob(t, ctx, store, "package payment\nconst Listed = true\n")
	first := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/listed_one.go", blobID, hash)
	createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/listed_two.go", blobID, hash)
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}

	req := &corev1.ListChangesetsRequest{AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"}}
	full, err := store.Changesets().List(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	summaryReq := proto.Clone(req).(*corev1.ListChangesetsRequest)
	summaryReq.Summary = true
	summaries, err := store.Changesets().List(ctx, summaryReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 2 || len(summaries) != len(full) {
		t.Fatalf("summaries = %d, full = %d", len(summaries), len(full))
	}
	for i := range full {
		if len(summaries[i].Patchsets) != 0 {
			t.Fatalf("summary %s has patchsets", summaries[i].Id)
		}
		want := proto.Clone(full[i]).(*corev1.Changeset)
		want.Patchsets = nil
		if !proto.Equal(summaries[i], want) {
			t.Fatalf("summary %d differs from the full changeset:\n got %v\nwant %v", i, summaries[i], want)
		}
	}
}
