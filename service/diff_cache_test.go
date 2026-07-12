package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestDiffChangesetPathsAndCache(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	mem.PutSlice(sliceRef, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "diff cache test",
	})
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{
		"/acme/payment/a.txt",
		"/acme/payment/b.txt",
		"/acme/payment/c.txt",
	}
	edits := make([]*corev1.FileEdit, 0, len(paths))
	for i, p := range paths {
		blob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
			Data:  []byte(fmt.Sprintf("file %d\n", i+1)),
			Slice: sliceRef,
		})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, &corev1.FileEdit{
			Op:          "add",
			Path:        p,
			BlobId:      blob.BlobId,
			ContentHash: blob.ContentHash,
			Mode:        0o100644,
		})
	}
	firstPatchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    edits,
	})
	if err != nil {
		t.Fatal(err)
	}

	countingStore := &countingDiffCacheStore{ObjectStore: mem.Objects}
	handlers.Changeset.ObjectStore = countingStore

	filtered, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    firstPatchset.Id,
		Paths:       []string{paths[1]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(filtered.ChangedPaths, paths) {
		t.Fatalf("filtered ChangedPaths = %v, want %v", filtered.ChangedPaths, paths)
	}
	if !strings.Contains(filtered.Diff, paths[1]) || !strings.Contains(filtered.Diff, "+file 2") {
		t.Fatalf("filtered diff does not contain file B:\n%s", filtered.Diff)
	}
	if strings.Contains(filtered.Diff, paths[0]) || strings.Contains(filtered.Diff, paths[2]) {
		t.Fatalf("filtered diff contains an unrequested file:\n%s", filtered.Diff)
	}

	bogus, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    firstPatchset.Id,
		Paths:       []string{"/acme/payment/not-changed.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bogus.Diff != "" {
		t.Fatalf("bogus-path diff = %q, want empty", bogus.Diff)
	}
	if !slices.Equal(bogus.ChangedPaths, paths) {
		t.Fatalf("bogus-path ChangedPaths = %v, want %v", bogus.ChangedPaths, paths)
	}
	if got := countingStore.cacheGets.Load(); got != 0 {
		t.Fatalf("filtered cache Get count = %d, want 0", got)
	}
	if got := countingStore.cachePuts.Load(); got != 0 {
		t.Fatalf("filtered cache Put count = %d, want 0", got)
	}

	full, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    firstPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if full.Diff == "" {
		t.Fatal("full diff is empty")
	}
	firstKey := expectedDiffCacheKey(cs.Id, "", firstPatchset.Id)
	if got := readTestObject(t, mem.Objects, firstKey); got != full.Diff {
		t.Fatalf("cached diff mismatch:\n got: %q\nwant: %q", got, full.Diff)
	}
	if got := countingStore.cacheGets.Load(); got != 1 {
		t.Fatalf("full diff cache Get count = %d, want 1", got)
	}
	if got := countingStore.cachePuts.Load(); got != 1 {
		t.Fatalf("full diff cache Put count = %d, want 1", got)
	}

	const sentinel = "sentinel cached diff\n"
	mem.PutObject(firstKey, []byte(sentinel))
	cached, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    firstPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cached.Diff != sentinel {
		t.Fatalf("cached diff = %q, want sentinel", cached.Diff)
	}
	if got := countingStore.cachePuts.Load(); got != 1 {
		t.Fatalf("cache-hit Put count = %d, want 1", got)
	}

	secondBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("file 2 patchset 2\n"),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondPatchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: firstPatchset.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        paths[1],
			BlobId:      secondBlob.BlobId,
			ContentHash: secondBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondKey := expectedDiffCacheKey(cs.Id, "", secondPatchset.Id)
	if secondKey == firstKey {
		t.Fatalf("different patchsets produced the same cache key %q", firstKey)
	}
	second, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    secondPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Diff == sentinel || !strings.Contains(second.Diff, "+file 2 patchset 2") {
		t.Fatalf("second patchset served stale or unexpected diff:\n%s", second.Diff)
	}
	if got := readTestObject(t, mem.Objects, secondKey); got != second.Diff {
		t.Fatalf("second cached diff mismatch:\n got: %q\nwant: %q", got, second.Diff)
	}
}

type countingDiffCacheStore struct {
	ObjectStore
	cacheGets atomic.Int64
	cachePuts atomic.Int64
}

func (s *countingDiffCacheStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if strings.HasPrefix(key, "cache/diff/") {
		s.cacheGets.Add(1)
	}
	return s.ObjectStore.Get(ctx, key, offset, length)
}

func (s *countingDiffCacheStore) Put(ctx context.Context, key string, r io.Reader) error {
	if strings.HasPrefix(key, "cache/diff/") {
		s.cachePuts.Add(1)
	}
	return s.ObjectStore.Put(ctx, key, r)
}

func expectedDiffCacheKey(changesetID, fromPatchsetID, toPatchsetID string) string {
	digest := sha256.Sum256([]byte(changesetID + "|" + fromPatchsetID + "|" + toPatchsetID))
	return fmt.Sprintf("cache/diff/v1/%x", digest)
}

func readTestObject(t *testing.T, store ObjectStore, key string) string {
	t.Helper()
	r, err := store.Get(context.Background(), key, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
