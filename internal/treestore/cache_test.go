package treestore

import (
	"context"
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

	next, err := store.ApplyEdits(ctx, root, []FileEdit{upsert("/acme/x", "3")})
	if err != nil {
		t.Fatal(err)
	}
	if got := hashAt(root, "/acme/x"); got != "sha256:1" {
		t.Fatalf("the old tree reads %s after a write", got)
	}
	if got := hashAt(next, "/acme/x"); got != "sha256:3" {
		t.Fatalf("the new tree reads %s", got)
	}
}

func (m *memoryObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
