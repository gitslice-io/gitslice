package postgres

import (
	"reflect"
	"testing"
)

// GetCommits reads what GetCommit does, many at once; an id with no commit is
// left out.
func TestGetCommitsMatchesGetCommit(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	patchset := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/a.go", blobID, contentHash)
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Changesets().PublishPending(ctx, 10); err != nil || n != 1 {
		t.Fatalf("publish: %d, %v", n, err)
	}
	head := getTestRef(t, ctx, store)

	got, err := store.Repository().GetCommits(ctx, []string{head.CommitId, base.CommitId, "sha256:missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("GetCommits returned %d commits, want 2 (the missing id left out): %v", len(got), got)
	}
	for _, id := range []string{head.CommitId, base.CommitId} {
		want, err := store.Repository().GetCommit(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[id].GetParentIds(), want.GetParentIds()) ||
			got[id].GetRootTreeId() != want.GetRootTreeId() ||
			got[id].GetMessage() != want.GetMessage() ||
			got[id].GetCreatedAt() != want.GetCreatedAt() ||
			!reflect.DeepEqual(got[id].GetChangedPaths(), want.GetChangedPaths()) {
			t.Fatalf("GetCommits[%s] = %v, want %v", id, got[id], want)
		}
	}
	if got[head.CommitId] == nil || len(got[head.CommitId].GetChangedPaths()) == 0 {
		t.Fatalf("the published commit should list its changed paths: %v", got[head.CommitId])
	}
	if empty, err := store.Repository().GetCommits(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("no ids: %v, %v", empty, err)
	}
}
