package postgres

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestStoragePublishesObjectStoreTreeAndReadsFiles(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	path := "/acme/payment/a.go"

	patchset := createDraftPatchset(t, ctx, store, base.CommitId, path, blobID, contentHash)
	submit, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id)
	if err != nil {
		t.Fatal(err)
	}
	if submit.Status != "pending_publish" {
		t.Fatalf("submit status = %q, want pending_publish", submit.Status)
	}
	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	ref := getTestRef(t, ctx, store)
	if ref.CommitId == base.CommitId {
		t.Fatal("ref did not move after publish")
	}
	commit, err := store.Repository().GetCommit(ctx, ref.CommitId)
	if err != nil {
		t.Fatal(err)
	}
	if commit.RootTreeId == "" {
		t.Fatal("published commit has empty root_tree_id")
	}
	entry, err := store.Repository().GetFile(ctx, ref.CommitId, path)
	if err != nil {
		t.Fatal(err)
	}
	if entry.BlobID != blobID || entry.ContentHash != contentHash {
		t.Fatalf("entry = %#v, want blob=%s hash=%s", entry, blobID, contentHash)
	}
	files, err := store.Repository().ListFiles(ctx, ref.CommitId, "/acme/payment")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != path {
		t.Fatalf("files = %#v, want only %s", files, path)
	}
}

func TestStoragePublishEnqueuesOutboxAndDrainIndexesChangedPaths(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlob(t, ctx, store, "package payment\nconst Indexed = true\n")
	path := "/acme/payment/indexed.go"

	patchset := createDraftPatchset(t, ctx, store, base.CommitId, path, blobID, contentHash)
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	commitID := getTestRef(t, ctx, store).CommitId
	if depth := outboxDepthForTest(t, ctx, store); depth != 1 {
		t.Fatalf("outbox depth = %d, want 1", depth)
	}
	if count := commitChangedPathCountForTest(t, ctx, store, commitID, path); count != 0 {
		t.Fatalf("commit_changed_paths before drain = %d, want 0", count)
	}

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.Changesets().WaitForOutboxDrain(drainCtx); err != nil {
		t.Fatal(err)
	}
	if depth := outboxDepthForTest(t, ctx, store); depth != 0 {
		t.Fatalf("outbox depth after drain = %d, want 0", depth)
	}
	if count := commitChangedPathCountForTest(t, ctx, store, commitID, path); count != 1 {
		t.Fatalf("commit_changed_paths after drain = %d, want 1", count)
	}
}

func TestStorageOutboxFailureIncrementsAttemptsAndRetries(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	var id int64
	if err := store.db.QueryRowContext(ctx, `
		insert into outbox(kind, payload)
		values ('unknown_kind', '{}'::jsonb)
		returning id
	`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	result, err := store.Changesets().ProcessOutbox(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 0 || result.Failed != 1 {
		t.Fatalf("ProcessOutbox result = %#v, want 0 processed and 1 failed", result)
	}
	if attempts := outboxAttemptsForTest(t, ctx, store, id); attempts != 1 {
		t.Fatalf("attempts after first failure = %d, want 1", attempts)
	}

	result, err = store.Changesets().ProcessOutbox(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 0 || result.Failed != 1 {
		t.Fatalf("retry ProcessOutbox result = %#v, want 0 processed and 1 failed", result)
	}
	if attempts := outboxAttemptsForTest(t, ctx, store, id); attempts != 2 {
		t.Fatalf("attempts after retry = %d, want 2", attempts)
	}
}

func TestStorageRebuildDerivedIndexesMatchesIncrementalChangedPaths(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	first := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/rebuild_a.go", firstBlobID, firstHash)
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("first published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst B = 1\n")
	second := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/rebuild_b.go", secondBlobID, secondHash)
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("second published = %d, want 1", published)
	}
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.Changesets().WaitForOutboxDrain(drainCtx); err != nil {
		t.Fatal(err)
	}
	incremental := commitChangedPathRowsForTest(t, ctx, store, DefaultTargetRef)
	if len(incremental) < 2 {
		t.Fatalf("incremental changed-path rows = %#v, want at least 2", incremental)
	}

	if err := store.Changesets().RebuildDerivedIndexes(ctx, DefaultTargetRef); err != nil {
		t.Fatal(err)
	}
	rebuilt := commitChangedPathRowsForTest(t, ctx, store, DefaultTargetRef)
	if !reflect.DeepEqual(rebuilt, incremental) {
		t.Fatalf("rebuilt changed-path rows differ\nincremental=%#v\nrebuilt=%#v", incremental, rebuilt)
	}
}
