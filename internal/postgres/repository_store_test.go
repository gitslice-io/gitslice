package postgres

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type countingTreeObjectStore struct {
	base treestore.ObjectStore
	gets atomic.Int64
}

func (s *countingTreeObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	return s.base.Put(ctx, key, r)
}

func (s *countingTreeObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	s.gets.Add(1)
	return s.base.Get(ctx, key, offset, length)
}

func (s *countingTreeObjectStore) resetGets() {
	s.gets.Store(0)
}

func (s *countingTreeObjectStore) getCount() int64 {
	return s.gets.Load()
}

func TestRepositoryStoreGetFileAtMaterializedHeadUsesFlatIndex(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)
	counter := useCountingTreeStore(store, objects)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlob(t, ctx, store, "flat index head file\n")
	filePath := "/acme/payment/deep/one/two/head.txt"
	headCommitID := publishRepositoryTestEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        filePath,
		BlobId:      blobID,
		ContentHash: contentHash,
		Mode:        0o100644,
	}})
	requireMaterializedHead(t, ctx, store, headCommitID)

	rootTreeID, err := store.Repository().RootTreeForCommit(ctx, headCommitID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := store.Repository().GetFileAtTree(ctx, rootTreeID, filePath)
	if err != nil {
		t.Fatal(err)
	}

	counter.resetGets()
	got, err := store.Repository().GetFile(ctx, headCommitID, filePath)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Fatalf("GetFile = %#v, want tree entry %#v", got, want)
	}
	if gotReads := counter.getCount(); gotReads != 0 {
		t.Fatalf("tree object reads = %d, want 0 for materialized-head file read", gotReads)
	}
}

func TestRepositoryStoreGetFileHistoricalCommitFallsBackToTree(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)
	counter := useCountingTreeStore(store, objects)
	base := getTestRef(t, ctx, store)
	filePath := "/acme/payment/deep/history/file.txt"
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "historical content\n")
	firstCommitID := publishRepositoryTestEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        filePath,
		BlobId:      firstBlobID,
		ContentHash: firstHash,
		Mode:        0o100644,
	}})

	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "head content\n")
	secondCommitID := publishRepositoryTestEdits(t, ctx, store, firstCommitID, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        filePath,
		BlobId:      secondBlobID,
		ContentHash: secondHash,
		Mode:        0o100755,
	}})
	requireMaterializedHead(t, ctx, store, secondCommitID)

	counter.resetGets()
	got, err := store.Repository().GetFile(ctx, firstCommitID, filePath)
	if err != nil {
		t.Fatal(err)
	}
	if got.BlobID != firstBlobID || got.ContentHash != firstHash || got.Mode != 0o100644 {
		t.Fatalf("historical GetFile = %#v, want first blob/hash/mode", got)
	}
	if gotReads := counter.getCount(); gotReads == 0 {
		t.Fatal("tree object reads = 0, want historical commit read to fall back to tree")
	}
}

func TestRepositoryStoreGetFileStaleMarkerFallsBackToNewHeadTree(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)
	counter := useCountingTreeStore(store, objects)
	base := getTestRef(t, ctx, store)
	filePath := "/acme/payment/deep/stale/file.txt"
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "old materialized content\n")
	firstCommitID := publishRepositoryTestEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        filePath,
		BlobId:      firstBlobID,
		ContentHash: firstHash,
		Mode:        0o100644,
	}})
	requireMaterializedHead(t, ctx, store, firstCommitID)

	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "new unmaterialized content\n")
	secondCommitID := publishRepositoryTestEdits(t, ctx, store, firstCommitID, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        filePath,
		BlobId:      secondBlobID,
		ContentHash: secondHash,
		Mode:        0o100755,
	}})

	counter.resetGets()
	got, err := store.Repository().GetFile(ctx, secondCommitID, filePath)
	if err != nil {
		t.Fatal(err)
	}
	if got.BlobID != secondBlobID || got.ContentHash != secondHash || got.Mode != 0o100755 {
		t.Fatalf("stale-marker GetFile = %#v, want new head blob/hash/mode", got)
	}
	if gotReads := counter.getCount(); gotReads == 0 {
		t.Fatal("tree object reads = 0, want stale marker read to fall back to tree")
	}
}

func TestRepositoryStoreGetEntryDirectoryAtMaterializedHeadFallsBackToTree(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)
	counter := useCountingTreeStore(store, objects)
	base := getTestRef(t, ctx, store)
	dirPath := "/acme/payment/materialized_dir"
	headCommitID := publishRepositoryTestEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:   "mkdir",
		Path: dirPath,
	}})
	requireMaterializedHead(t, ctx, store, headCommitID)

	rootTreeID, err := store.Repository().RootTreeForCommit(ctx, headCommitID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := store.Repository().GetEntryAtTree(ctx, rootTreeID, dirPath)
	if err != nil {
		t.Fatal(err)
	}

	counter.resetGets()
	got, err := store.Repository().GetEntry(ctx, headCommitID, dirPath)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Fatalf("GetEntry directory = %#v, want tree entry %#v", got, want)
	}
	if got.Kind != "directory" || got.TreeID == "" {
		t.Fatalf("GetEntry directory = %#v, want directory with non-empty TreeID", got)
	}
	if gotReads := counter.getCount(); gotReads == 0 {
		t.Fatal("tree object reads = 0, want directory read to fall back to tree")
	}
}

func TestRepositoryStoreGetFileMissingPathAtMaterializedHead(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)
	counter := useCountingTreeStore(store, objects)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlob(t, ctx, store, "present content\n")
	headCommitID := publishRepositoryTestEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        "/acme/payment/present.txt",
		BlobId:      blobID,
		ContentHash: contentHash,
		Mode:        0o100644,
	}})
	requireMaterializedHead(t, ctx, store, headCommitID)

	counter.resetGets()
	_, err := store.Repository().GetFile(ctx, headCommitID, "/acme/payment/missing.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing GetFile err = %v, want ErrNotFound", err)
	}
	if gotReads := counter.getCount(); gotReads != 0 {
		t.Fatalf("tree object reads = %d, want 0 for materialized-head missing file", gotReads)
	}

	counter.resetGets()
	_, err = store.Repository().GetEntry(ctx, headCommitID, "/acme/payment/missing.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing GetEntry err = %v, want ErrNotFound", err)
	}
}

func useCountingTreeStore(store *DB, base treestore.ObjectStore) *countingTreeObjectStore {
	counter := &countingTreeObjectStore{base: base}
	store.SetTreeStore(treestore.New(counter))
	return counter
}

func publishRepositoryTestEdits(t *testing.T, ctx context.Context, store *DB, baseCommitID string, edits []*corev1.FileEdit) string {
	t.Helper()
	patchset := createDraftPatchsetWithEdits(t, ctx, store, baseCommitID, edits)
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	return getTestRef(t, ctx, store).CommitId
}

func requireMaterializedHead(t *testing.T, ctx context.Context, store *DB, wantCommitID string) {
	t.Helper()
	if err := store.Changesets().RebuildDerivedIndexes(ctx, DefaultTargetRef); err != nil {
		t.Fatal(err)
	}
	gotCommitID, ok, err := store.Repository().materializedHeadCommit(ctx, DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("materialized head marker was not recorded")
	}
	if gotCommitID != wantCommitID {
		t.Fatalf("materialized head = %s, want %s", gotCommitID, wantCommitID)
	}
}
