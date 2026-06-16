package treestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
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

func TestApplyEditsBatchesFilesAndEmptyDirectories(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjectStore()
	store := New(objects)
	edits := make([]FileEdit, 0, 110)
	for i := 0; i < 100; i++ {
		p := fmt.Sprintf("/acme/payment/bulk/dir-%02d/file-%03d.txt", i%10, i)
		edits = append(edits, FileEdit{
			Op:   "upsert",
			Path: p,
			File: &FileEntry{Path: p, BlobID: fmt.Sprintf("blob-%03d", i), ContentHash: fmt.Sprintf("sha256:%03d", i), Mode: 0o100644, Size: int64(i)},
		})
	}
	for i := 0; i < 10; i++ {
		edits = append(edits, FileEdit{Op: "mkdir", Path: fmt.Sprintf("/acme/payment/bulk/empty-%02d", i)})
	}
	root, err := store.ApplyEdits(ctx, EmptyRootID(), edits)
	if err != nil {
		t.Fatal(err)
	}
	if objects.putCount() > 20 {
		t.Fatalf("batched apply wrote %d tree objects, want at most 20", objects.putCount())
	}
	files, err := store.ListFiles(ctx, root, "/acme/payment/bulk")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 100 {
		t.Fatalf("files = %d, want 100", len(files))
	}
	for i := 0; i < 10; i++ {
		entry, err := store.GetEntry(ctx, root, fmt.Sprintf("/acme/payment/bulk/empty-%02d", i))
		if err != nil {
			t.Fatal(err)
		}
		if entry.Kind != "directory" {
			t.Fatalf("empty dir kind = %q, want directory", entry.Kind)
		}
	}
}

func TestApplyEditsFlushesBufferedTreeNodesToBase(t *testing.T) {
	ctx := context.Background()
	edits := []FileEdit{{
		Op:   "upsert",
		Path: "/acme/payment/a.go",
		File: &FileEntry{Path: "/acme/payment/a.go", BlobID: "blob-a", ContentHash: "sha256:a", Mode: 0o100644, Size: 1},
	}, {
		Op:   "upsert",
		Path: "/acme/payment/nested/b.go",
		File: &FileEntry{Path: "/acme/payment/nested/b.go", BlobID: "blob-b", ContentHash: "sha256:b", Mode: 0o100644, Size: 2},
	}, {
		Op:   "upsert",
		Path: "/acme/billing/c.go",
		File: &FileEntry{Path: "/acme/billing/c.go", BlobID: "blob-c", ContentHash: "sha256:c", Mode: 0o100644, Size: 3},
	}, {
		Op:   "upsert",
		Path: "/zenith/ops/d.go",
		File: &FileEntry{Path: "/zenith/ops/d.go", BlobID: "blob-d", ContentHash: "sha256:d", Mode: 0o100644, Size: 4},
	}, {
		Op:   "mkdir",
		Path: "/acme/payment/empty",
	}}

	expectedObjects := newMemoryObjectStore()
	expectedStore := New(expectedObjects)
	expectedRoot, err := expectedStore.applyEditsSequential(ctx, EmptyRootID(), edits)
	if err != nil {
		t.Fatal(err)
	}

	objects := newMemoryObjectStore()
	store := New(objects)
	root, err := store.ApplyEdits(ctx, EmptyRootID(), edits)
	if err != nil {
		t.Fatal(err)
	}
	if root != expectedRoot {
		t.Fatalf("root = %s, want %s", root, expectedRoot)
	}
	if got := objects.getCount(Key(EmptyRootID())); got != 0 {
		t.Fatalf("base Get(%q) count = %d, want 0", Key(EmptyRootID()), got)
	}

	gotFiles, err := store.ListFiles(ctx, root, "/")
	if err != nil {
		t.Fatal(err)
	}
	wantFiles, err := expectedStore.ListFiles(ctx, expectedRoot, "/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Fatalf("files mismatch:\n got: %#v\nwant: %#v", gotFiles, wantFiles)
	}

	keys := walkTreeKeysFromBase(t, objects, root)
	if len(keys) == 0 {
		t.Fatal("expected at least one persisted tree node")
	}
	for _, key := range keys {
		if !objects.hasObject(key) {
			t.Fatalf("reachable tree node %q was not persisted to base", key)
		}
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
	putKeys []string
	getKeys []string
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
	m.putKeys = append(m.putKeys, key)
	m.objects[key] = data
	return nil
}

func (m *memoryObjectStore) Get(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getKeys = append(m.getKeys, key)
	data, ok := m.objects[key]
	if !ok {
		return nil, errors.New("missing object")
	}
	start := offset
	if start < 0 {
		start = 0
	}
	if start > int64(len(data)) {
		start = int64(len(data))
	}
	end := int64(len(data))
	if length > 0 && start+length < end {
		end = start + length
	}
	return io.NopCloser(bytes.NewReader(data[int(start):int(end)])), nil
}

func (m *memoryObjectStore) putCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts
}

func (m *memoryObjectStore) getCount(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int
	for _, got := range m.getKeys {
		if got == key {
			count++
		}
	}
	return count
}

func (m *memoryObjectStore) hasObject(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok
}

func walkTreeKeysFromBase(t *testing.T, objects *memoryObjectStore, rootTreeID string) []string {
	t.Helper()
	visited := map[string]struct{}{}
	var keys []string
	var walk func(string)
	walk = func(treeID string) {
		t.Helper()
		if treeID == "" || treeID == EmptyRootID() {
			return
		}
		if _, ok := visited[treeID]; ok {
			return
		}
		visited[treeID] = struct{}{}
		key := Key(treeID)
		rc, err := objects.Get(context.Background(), key, 0, 0)
		if err != nil {
			t.Fatalf("tree node %q was not gettable from base: %v", key, err)
		}
		defer rc.Close()
		var node Node
		if err := json.NewDecoder(rc).Decode(&node); err != nil {
			t.Fatalf("decode tree node %q: %v", key, err)
		}
		keys = append(keys, key)
		for _, entry := range node.Entries {
			if entry.Kind == "directory" {
				walk(entry.TreeID)
			}
		}
	}
	walk(rootTreeID)
	return keys
}
