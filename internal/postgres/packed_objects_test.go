package postgres

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/objectstore/packed"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestPackedObjectStoreAndColdRead(t *testing.T) {
	ctx, store, fsStore := newPostgresTestStoreWithObjects(t)

	packedStore := packed.New(fsStore, store.PackedObjects())
	pw, ok := packedStore.(*packed.Store)
	if !ok {
		t.Fatal("packed.New did not return *packed.Store")
	}
	objects := map[string][]byte{
		"blobs/sha256:compressible": []byte(strings.Repeat("hello packed world ", 200)),
		"blobs/sha256:tiny":         []byte("tiny"),
		"trees/sha256:tree1":        []byte(`{"entries":[{"name":"README.md","kind":"file"}]}`),
	}
	if err := pw.PutPack(ctx, "packs/blobs/test.pack", objects); err != nil {
		t.Fatalf("PutPack: %v", err)
	}

	// Simulate a cold restart with a fresh packed.Store instance (empty LRU cache).
	coldStore := packed.New(fsStore, store.PackedObjects())
	for key, want := range objects {
		rc, err := coldStore.Get(ctx, key, 0, 0)
		if err != nil {
			t.Fatalf("cold Get(%s): %v", key, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read cold Get(%s): %v", key, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("cold Get(%s) mismatch: got %d bytes, want %d", key, len(got), len(want))
		}
	}

	// Range read on a compressed packed blob.
	rc, err := coldStore.Get(ctx, "blobs/sha256:compressible", 6, 6)
	if err != nil {
		t.Fatalf("range Get: %v", err)
	}
	slice, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read range Get: %v", err)
	}
	if string(slice) != "packed" {
		t.Fatalf("range Get = %q, want %q", string(slice), "packed")
	}

	// DeleteKey removes the index entry cleanly.
	if err := store.PackedObjects().DeleteKey(ctx, "blobs/sha256:tiny"); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if _, ok, err := store.PackedObjects().Lookup(ctx, "blobs/sha256:tiny"); err != nil {
		t.Fatalf("Lookup after delete: %v", err)
	} else if ok {
		t.Fatal("expected deleted key to be absent from packed_objects")
	}
}

func TestPublishGitImportBatchPacksTreesAndDrainsBatchOutbox(t *testing.T) {
	ctx, store, fsStore := newPostgresTestStoreWithObjects(t)
	packedStore := packed.New(fsStore, store.PackedObjects())
	store.SetTreeStore(treestore.New(packedStore))

	blobA, hashA := upsertTestBlob(t, ctx, store, "package payment\nconst V = 1\n")
	blobB, hashB := upsertTestBlob(t, ctx, store, "package payment\nconst V = 2\n")
	blobC, hashC := upsertTestBlob(t, ctx, store, "package payment\nconst C = 3\n")

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	imp, err := store.Repository().GetOrCreateGitImport(ctx, "user_alice", "https://example.com/repo.git", "/acme/payment", sliceRef, "slice_acme_payment", DefaultTargetRef, "deep", 3)
	if err != nil {
		t.Fatalf("GetOrCreateGitImport: %v", err)
	}

	// Publish 3 commits in one batch: create files, update one, delete one.
	published, err := store.Changesets().PublishGitImportBatch(ctx, storage.GitImportBatchInput{
		SubjectID: "user_alice",
		TargetRef: DefaultTargetRef,
		ImportID:  imp.ID,
		Commits: []storage.GitImportCommitSpec{
			{
				GitCommitID:  "git-commit-1",
				Position:     1,
				Title:        "initial",
				ChangedPaths: []string{"/acme/payment/a.go", "/acme/payment/sub/c.go"},
				FileEdits: []*corev1.FileEdit{
					{Op: "upsert", Path: "/acme/payment/a.go", BlobId: blobA, Mode: 0o100644},
					{Op: "upsert", Path: "/acme/payment/sub/c.go", BlobId: blobC, Mode: 0o100644},
				},
			},
			{
				GitCommitID:  "git-commit-2",
				Position:     2,
				Title:        "update a",
				ChangedPaths: []string{"/acme/payment/a.go"},
				FileEdits: []*corev1.FileEdit{
					{Op: "upsert", Path: "/acme/payment/a.go", BlobId: blobB, Mode: 0o100644},
				},
			},
			{
				GitCommitID:  "git-commit-3",
				Position:     3,
				Title:        "delete c",
				ChangedPaths: []string{"/acme/payment/sub/c.go"},
				FileEdits: []*corev1.FileEdit{
					{Op: "delete", Path: "/acme/payment/sub/c.go"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("PublishGitImportBatch: %v", err)
	}
	if len(published) != 3 {
		t.Fatalf("published = %d, want 3", len(published))
	}

	// commit_changed_paths should already be indexed synchronously inside the publish tx.
	if count := commitChangedPathCountForTest(t, ctx, store, published[0].NativeCommitID, "/acme/payment/a.go"); count != 1 {
		t.Fatalf("commit_changed_paths for commit 1 = %d, want 1", count)
	}
	if count := commitChangedPathCountForTest(t, ctx, store, published[2].NativeCommitID, "/acme/payment/sub/c.go"); count != 1 {
		t.Fatalf("commit_changed_paths for commit 3 = %d, want 1", count)
	}

	// Only 1 collapsed outbox row should be enqueued for the whole batch.
	if depth := outboxDepthForTest(t, ctx, store); depth != 1 {
		t.Fatalf("outbox depth = %d, want 1", depth)
	}

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.Changesets().WaitForOutboxDrain(drainCtx); err != nil {
		t.Fatalf("WaitForOutboxDrain: %v", err)
	}
	if depth := outboxDepthForTest(t, ctx, store); depth != 0 {
		t.Fatalf("outbox depth after drain = %d, want 0", depth)
	}

	// Targeted git import commit lookup should find only requested git IDs.
	byIDs, err := store.Repository().ListGitImportCommitsByGitIDs(ctx, imp.ID, []string{"git-commit-1", "git-commit-3", "missing"})
	if err != nil {
		t.Fatalf("ListGitImportCommitsByGitIDs: %v", err)
	}
	if len(byIDs) != 2 || byIDs[0].GitCommitID != "git-commit-1" || byIDs[1].GitCommitID != "git-commit-3" {
		t.Fatalf("ListGitImportCommitsByGitIDs = %#v, want commits 1 and 3", byIDs)
	}

	// Verify final files read back cleanly (including cold packed tree reads).
	store.SetTreeStore(treestore.New(packed.New(fsStore, store.PackedObjects())))
	head := getTestRef(t, ctx, store)
	entryA, err := store.Repository().GetFile(ctx, head.CommitId, "/acme/payment/a.go")
	if err != nil {
		t.Fatalf("GetFile a.go: %v", err)
	}
	if entryA.BlobID != blobB || entryA.ContentHash != hashB {
		t.Fatalf("a.go = %#v, want blob=%s hash=%s", entryA, blobB, hashB)
	}
	if _, err := store.Repository().GetFile(ctx, head.CommitId, "/acme/payment/sub/c.go"); err != ErrNotFound {
		t.Fatalf("GetFile deleted c.go err = %v, want ErrNotFound", err)
	}

	// Verify historical commit 1 still reads original blobA and c.go from packed trees.
	histA, err := store.Repository().GetFile(ctx, published[0].NativeCommitID, "/acme/payment/a.go")
	if err != nil || histA.ContentHash != hashA {
		t.Fatalf("historical commit 1 a.go = %#v, err=%v, want hash=%s", histA, err, hashA)
	}
	histC, err := store.Repository().GetFile(ctx, published[0].NativeCommitID, "/acme/payment/sub/c.go")
	if err != nil || histC.ContentHash != hashC {
		t.Fatalf("historical commit 1 c.go = %#v, err=%v, want hash=%s", histC, err, hashC)
	}

	// Verify current_path_entities reflects final state (a.go present, sub/c.go deleted).
	var aCount, cCount int
	if err := store.db.QueryRowContext(ctx, `select count(*) from current_path_entities where target_ref = $1 and path = '/acme/payment/a.go'`, DefaultTargetRef).Scan(&aCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `select count(*) from current_path_entities where target_ref = $1 and path = '/acme/payment/sub/c.go'`, DefaultTargetRef).Scan(&cCount); err != nil {
		t.Fatal(err)
	}
	if aCount != 1 || cCount != 0 {
		t.Fatalf("current_path_entities counts: a.go=%d c.go=%d, want 1 and 0", aCount, cCount)
	}
}
