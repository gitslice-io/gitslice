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
	// A reader that did not write the trees, as after a restart.
	reader := New(cache.New(inner, 1<<20, 1<<20))
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

func (m *memoryObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
