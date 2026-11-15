package treestore

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"gitslice.io/gitslice/internal/objectstore/cache"
)

// TestTreeReadsStayCached: tree nodes read through the object cache are kept,
// so reading the same path again does not go back to object storage. (A JSON
// decoder reading straight from the object stopped short of EOF, which the
// cache waits for, so every tree read went to storage.)
func TestTreeReadsStayCached(t *testing.T) {
	ctx := context.Background()
	inner := newMemoryObjectStore()
	writer := New(inner)
	root, err := writer.ApplyEdits(ctx, EmptyRootID(), []FileEdit{{
		Op:   "upsert",
		Path: "/acme/payment/api/handler.go",
		File: &FileEntry{Path: "/acme/payment/api/handler.go", BlobID: "blob", ContentHash: "sha256:h", Mode: 0o100644, Size: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// A reader that did not write the trees, as after a restart, without the
	// decoded-node cache so the object cache is what is tested.
	reader := &Store{objects: cache.New(inner, 1<<20, 1<<20)}
	reads := func() int {
		inner.mu.Lock()
		defer inner.mu.Unlock()
		return len(inner.getKeys)
	}
	before := reads()
	if _, err := reader.GetFile(ctx, root, "/acme/payment/api/handler.go"); err != nil {
		t.Fatal(err)
	}
	first := reads() - before
	if first == 0 {
		t.Fatal("the first read did not reach storage")
	}
	if _, err := reader.GetFile(ctx, root, "/acme/payment/api/handler.go"); err != nil {
		t.Fatal(err)
	}
	if again := reads() - before - first; again != 0 {
		t.Fatalf("reading the same path again went to storage %d times", again)
	}
}

// TestNodeCacheSharesReadsNotWrites: path lookups decode a node once, and
// neither a caller changing a node it read for writing nor a write starting
// from cached nodes changes what readers see.
func TestNodeCacheSharesReadsNotWrites(t *testing.T) {
	ctx := context.Background()
	inner := newMemoryObjectStore()
	store := New(inner)
	upsert := func(p, hash string) FileEdit {
		return FileEdit{Op: "upsert", Path: p, File: &FileEntry{Path: p, BlobID: "blob" + hash, ContentHash: "sha256:" + hash, Mode: 0o100644, Size: 1}}
	}
	root, err := store.ApplyEdits(ctx, EmptyRootID(), []FileEdit{upsert("/acme/x", "1"), upsert("/acme/y", "2")})
	if err != nil {
		t.Fatal(err)
	}
	hashAt := func(rootID, p string) string {
		t.Helper()
		file, err := store.GetFile(ctx, rootID, p)
		if err != nil {
			t.Fatal(err)
		}
		return file.ContentHash
	}
	hashAt(root, "/acme/x")
	inner.mu.Lock()
	before := len(inner.getKeys)
	inner.mu.Unlock()
	for range 10 {
		hashAt(root, "/acme/y")
	}
	inner.mu.Lock()
	again := len(inner.getKeys) - before
	inner.mu.Unlock()
	if again != 0 {
		t.Fatalf("repeated lookups read %d nodes from storage", again)
	}

	node, err := store.readNode(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	node.Entries[0].Name = "changed"
	if shared, _ := store.readSharedNode(ctx, root); shared.Entries[0].Name != "acme" {
		t.Fatalf("changing a node read for writing changed the cached node: %q", shared.Entries[0].Name)
	}

	// ApplyEdits should reuse s.nodes on warm paths without hitting inner.Get.
	inner.mu.Lock()
	getsBeforeWrite := len(inner.getKeys)
	inner.mu.Unlock()

	next, err := store.ApplyEdits(ctx, root, []FileEdit{upsert("/acme/x", "3")})
	if err != nil {
		t.Fatal(err)
	}
	inner.mu.Lock()
	getsDuringWrite := len(inner.getKeys) - getsBeforeWrite
	inner.mu.Unlock()
	if getsDuringWrite != 0 {
		t.Fatalf("ApplyEdits read %d cached nodes from storage, want 0", getsDuringWrite)
	}

	if got := hashAt(root, "/acme/x"); got != "sha256:1" {
		t.Fatalf("the old tree reads %s after a write", got)
	}
	if got := hashAt(next, "/acme/x"); got != "sha256:3" {
		t.Fatalf("the new tree reads %s", got)
	}
}

type failingPutStore struct {
	*memoryObjectStore
	failPut atomic.Bool
}

func (f *failingPutStore) Put(ctx context.Context, key string, r io.Reader) error {
	if f.failPut.Load() {
		return errors.New("simulated object store Put failure")
	}
	return f.memoryObjectStore.Put(ctx, key, r)
}

func TestFailedFlushDoesNotPoisonNodeCache(t *testing.T) {
	ctx := context.Background()
	inner := &failingPutStore{memoryObjectStore: newMemoryObjectStore()}
	store := New(inner)

	root, err := store.ApplyEdits(ctx, EmptyRootID(), []FileEdit{treeFile("/acme/a.go")})
	if err != nil {
		t.Fatal(err)
	}
	entriesBefore := store.nodes.used

	inner.failPut.Store(true)
	if _, err := store.ApplyEdits(ctx, root, []FileEdit{treeFile("/acme/b.go")}); err == nil {
		t.Fatal("expected ApplyEdits to fail when flush Put fails")
	}
	if store.nodes.used != entriesBefore {
		t.Fatalf("failed flush mutated nodeCache: used = %d, want %d", store.nodes.used, entriesBefore)
	}
}

func TestSequentialEditsPruneUnreachableIntermediateNodes(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjectStore()
	store := New(objects)

	root, err := store.ApplyEdits(ctx, EmptyRootID(), []FileEdit{
		treeFile("/acme/payment/old.go"),
		treeFile("/acme/payment/keep.go"),
	})
	if err != nil {
		t.Fatal(err)
	}
	putsBefore := objects.putCount()

	// Rename triggers sequential deletePath + setEntry, creating intermediate
	// nodes for /acme/payment, /acme, and / that become unreachable once
	// new.go is inserted. Only the 3 final path-copied nodes should be flushed.
	renamed, err := store.ApplyEdits(ctx, root, []FileEdit{{
		Op:      "rename",
		OldPath: "/acme/payment/old.go",
		Path:    "/acme/payment/new.go",
	}})
	if err != nil {
		t.Fatal(err)
	}
	flushed := objects.putCount() - putsBefore
	if flushed != 3 {
		t.Fatalf("rename flushed %d tree blobs, want 3 (unreachable intermediates must be pruned)", flushed)
	}
	reachable := walkTreeKeysFromBase(t, objects, renamed)
	if len(reachable) == 0 {
		t.Fatal("expected reachable nodes in base store")
	}
}

func (m *memoryObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
