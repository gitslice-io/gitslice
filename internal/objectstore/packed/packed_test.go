package packed

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

type memObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	gets    int
}

func newMemObjectStore() *memObjectStore {
	return &memObjectStore{objects: map[string][]byte{}}
}

func (m *memObjectStore) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.objects[key] = data
	m.puts++
	m.mu.Unlock()
	return nil
}

func (m *memObjectStore) Get(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.gets++
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(sliceRange(data, offset, length))), nil
}

func (m *memObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.objects, key)
	m.mu.Unlock()
	return nil
}

type memIndex struct {
	mu      sync.Mutex
	entries map[string]Location
	lookups int
}

func newMemIndex() *memIndex {
	return &memIndex{entries: map[string]Location{}}
}

func (m *memIndex) Lookup(_ context.Context, objectKey string) (Location, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookups++
	loc, ok := m.entries[objectKey]
	return loc, ok, nil
}

func (m *memIndex) RecordPack(_ context.Context, packKey string, entries []IndexedEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range entries {
		if _, exists := m.entries[entry.ObjectKey]; !exists {
			m.entries[entry.ObjectKey] = Location{
				PackKey:   packKey,
				Offset:    entry.Offset,
				Length:    entry.Length,
				Codec:     entry.Codec,
				RawLength: entry.RawLength,
			}
		}
	}
	return nil
}

func (m *memIndex) DeleteKey(_ context.Context, objectKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, objectKey)
	return nil
}

func TestPutPackAndGetRoundTripZstdAndRaw(t *testing.T) {
	ctx := context.Background()
	base := newMemObjectStore()
	idx := newMemIndex()
	store := New(base, idx).(*Store)

	compressible := []byte(strings.Repeat("hello gitslice packed tree node\n", 100))
	tiny := []byte("small")
	empty := []byte{}

	objects := map[string][]byte{
		"trees/sha256:aaa": compressible,
		"blobs/sha256:bbb": tiny,
		"blobs/sha256:ccc": empty,
	}
	if err := store.PutPack(ctx, "packs/trees/p1.pack", objects); err != nil {
		t.Fatalf("PutPack: %v", err)
	}
	if base.puts != 1 {
		t.Fatalf("base puts = %d, want 1", base.puts)
	}
	if loc := idx.entries["trees/sha256:aaa"]; loc.Codec != CodecZstd || loc.Length >= int64(len(compressible)) {
		t.Fatalf("expected zstd compression for compressible object, got %+v", loc)
	}
	if loc := idx.entries["blobs/sha256:bbb"]; loc.Codec != CodecRaw {
		t.Fatalf("expected raw codec for tiny object, got %+v", loc)
	}

	// Read from a fresh Store with empty locCache to verify index lookup + range reads.
	readerStore := New(base, idx)
	for key, want := range objects {
		rc, err := readerStore.Get(ctx, key, 0, 0)
		if err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("ReadAll(%s): %v", key, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("Get(%s) mismatch: got %d bytes, want %d bytes", key, len(got), len(want))
		}
	}

	// Partial range read on zstd-compressed object.
	rc, err := readerStore.Get(ctx, "trees/sha256:aaa", 6, 8)
	if err != nil {
		t.Fatalf("range Get zstd: %v", err)
	}
	gotRange, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(gotRange) != "gitslice" {
		t.Fatalf("zstd range = %q, want %q", gotRange, "gitslice")
	}

	// Partial range read on raw object.
	rc, err = readerStore.Get(ctx, "blobs/sha256:bbb", 1, 3)
	if err != nil {
		t.Fatalf("range Get raw: %v", err)
	}
	gotRange, _ = io.ReadAll(rc)
	_ = rc.Close()
	if string(gotRange) != "mal" {
		t.Fatalf("raw range = %q, want %q", gotRange, "mal")
	}

	// Loose object fallback and non-packable key bypass.
	if err := base.Put(ctx, "blobs/sha256:loose", bytes.NewReader([]byte("loose-bytes"))); err != nil {
		t.Fatal(err)
	}
	rc, err = readerStore.Get(ctx, "blobs/sha256:loose", 0, 0)
	if err != nil {
		t.Fatalf("Get loose: %v", err)
	}
	gotLoose, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(gotLoose) != "loose-bytes" {
		t.Fatalf("loose = %q, want %q", gotLoose, "loose-bytes")
	}

	lookupsBefore := idx.lookups
	if err := readerStore.Put(ctx, "avatars/u1.png", bytes.NewReader([]byte("png"))); err != nil {
		t.Fatal(err)
	}
	rc, err = readerStore.Get(ctx, "avatars/u1.png", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if idx.lookups != lookupsBefore {
		t.Fatalf("non-packable key triggered index lookup (%d -> %d)", lookupsBefore, idx.lookups)
	}
}
