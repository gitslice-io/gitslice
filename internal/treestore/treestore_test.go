package treestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
)

func TestApplyEditsPathCopiesTreeNodes(t *testing.T) {
	ctx := context.Background()
	store := New(newMemoryObjectStore())
	if err := store.EnsureEmptyRoot(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := store.ApplyEdits(ctx, EmptyRootID(), []FileEdit{{
		Op:   "upsert",
		Path: "/acme/payment/a.go",
		File: &FileEntry{Path: "/acme/payment/a.go", BlobID: "blob-a", ContentHash: "sha256:a", Mode: 0o100644, Size: 1},
	}, {
		Op:   "upsert",
		Path: "/acme/payment/nested/b.go",
		File: &FileEntry{Path: "/acme/payment/nested/b.go", BlobID: "blob-b", ContentHash: "sha256:b", Mode: 0o100644, Size: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := store.ListFiles(ctx, root, "/acme/payment")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "/acme/payment/a.go" || files[1].Path != "/acme/payment/nested/b.go" {
		t.Fatalf("unexpected files: %#v", files)
	}
	updated, err := store.ApplyEdits(ctx, root, []FileEdit{{
		Op:   "upsert",
		Path: "/acme/payment/a.go",
		File: &FileEntry{Path: "/acme/payment/a.go", BlobID: "blob-a2", ContentHash: "sha256:a2", Mode: 0o100644, Size: 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if updated == root {
		t.Fatal("expected root tree id to change after file update")
	}
	entry, err := store.GetFile(ctx, updated, "/acme/payment/a.go")
	if err != nil {
		t.Fatal(err)
	}
	if entry.BlobID != "blob-a2" || entry.Size != 3 {
		t.Fatalf("unexpected updated entry: %#v", entry)
	}
	other, err := store.GetFile(ctx, updated, "/acme/payment/nested/b.go")
	if err != nil {
		t.Fatal(err)
	}
	if other.BlobID != "blob-b" {
		t.Fatalf("disjoint file changed unexpectedly: %#v", other)
	}
}

func TestApplyEditsBatchesSharedDirectoryWrites(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjectStore()
	store := New(objects)
	edits := make([]FileEdit, 0, 100)
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("/acme/payment/file-%03d.go", i)
		edits = append(edits, FileEdit{
			Op:   "upsert",
			Path: path,
			File: &FileEntry{Path: path, BlobID: fmt.Sprintf("blob-%03d", i), ContentHash: fmt.Sprintf("sha256:%03d", i), Mode: 0o100644, Size: int64(i)},
		})
	}
	root, err := store.ApplyEdits(ctx, EmptyRootID(), edits)
	if err != nil {
		t.Fatal(err)
	}
	if objects.putCount() > 4 {
		t.Fatalf("batched apply wrote %d tree objects, want at most 4", objects.putCount())
	}
	files, err := store.ListFiles(ctx, root, "/acme/payment")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(edits) {
		t.Fatalf("files = %d, want %d", len(files), len(edits))
	}
}

func TestRenameAndDelete(t *testing.T) {
	ctx := context.Background()
	store := New(newMemoryObjectStore())
	root, err := store.ApplyEdits(ctx, EmptyRootID(), []FileEdit{{
		Op:   "upsert",
		Path: "/acme/payment/old.go",
		File: &FileEntry{Path: "/acme/payment/old.go", BlobID: "blob-old", ContentHash: "sha256:old", Mode: 0o100644, Size: 7},
	}})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.ApplyEdits(ctx, root, []FileEdit{{
		Op:      "rename",
		OldPath: "/acme/payment/old.go",
		Path:    "/acme/payment/new.go",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetFile(ctx, root, "/acme/payment/old.go"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old path err = %v, want ErrNotFound", err)
	}
	renamed, err := store.GetFile(ctx, root, "/acme/payment/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.BlobID != "blob-old" {
		t.Fatalf("unexpected renamed entry: %#v", renamed)
	}
	root, err = store.ApplyEdits(ctx, root, []FileEdit{{Op: "delete", Path: "/acme/payment/new.go"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := store.ListFiles(ctx, root, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty tree after delete, got %#v", files)
	}
}

type memoryObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func newMemoryObjectStore() *memoryObjectStore {
	return &memoryObjectStore{objects: map[string][]byte{}}
}

func (m *memoryObjectStore) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.objects[key] = data
	return nil
}

func (m *memoryObjectStore) Get(_ context.Context, key string, _, _ int64) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, errors.New("missing object")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memoryObjectStore) putCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts
}
