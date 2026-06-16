package cache

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
)

type countingStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	gets    int
	puts    int
	deletes int
}

func newCountingStore() *countingStore {
	return &countingStore{objects: map[string][]byte{}}
}

func (c *countingStore) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	c.objects[key] = append([]byte(nil), data...)
	return nil
}

func (c *countingStore) Get(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	data, ok := c.objects[key]
	if !ok {
		return nil, io.EOF
	}
	ranged, err := objectRange(data, offset, length)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(ranged)), nil
}

func (c *countingStore) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes++
	delete(c.objects, key)
	return nil
}

func read(t *testing.T, s ObjectStore, key string, offset, length int64) []byte {
	t.Helper()
	rc, err := s.Get(context.Background(), key, offset, length)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWriteThroughThenServedFromCache(t *testing.T) {
	inner := newCountingStore()
	s := New(inner, 1<<20, 1<<20)
	if err := s.Put(context.Background(), "k", bytes.NewReader([]byte("hello world"))); err != nil {
		t.Fatal(err)
	}
	if inner.puts != 1 {
		t.Fatalf("inner puts = %d, want 1", inner.puts)
	}
	if got := read(t, s, "k", 0, 0); string(got) != "hello world" {
		t.Fatalf("got %q", got)
	}
	if inner.gets != 0 {
		t.Fatalf("inner gets = %d, want 0 (served from cache)", inner.gets)
	}
}

func TestRangedGetMatchesFullSlice(t *testing.T) {
	inner := newCountingStore()
	s := New(inner, 1<<20, 1<<20)
	_ = s.Put(context.Background(), "k", bytes.NewReader([]byte("0123456789")))
	if got := read(t, s, "k", 2, 3); string(got) != "234" {
		t.Fatalf("ranged got %q, want 234", got)
	}
	if got := read(t, s, "k", 8, 0); string(got) != "89" {
		t.Fatalf("to-end got %q, want 89", got)
	}
}

func TestMissReadsInnerAndIsNotCached(t *testing.T) {
	inner := newCountingStore()
	_ = inner.Put(context.Background(), "k", bytes.NewReader([]byte("abc")))
	innerGetsBefore := inner.gets
	s := New(inner, 1<<20, 1<<20)
	read(t, s, "k", 0, 0) // miss -> inner
	read(t, s, "k", 0, 0) // still miss (no read population) -> inner again
	if inner.gets-innerGetsBefore != 2 {
		t.Fatalf("inner gets delta = %d, want 2 (reads not cached)", inner.gets-innerGetsBefore)
	}
}

func TestEvictionRespectsMaxBytes(t *testing.T) {
	inner := newCountingStore()
	s := New(inner, 10, 10).(*Store)
	_ = s.Put(context.Background(), "a", bytes.NewReader([]byte("123456"))) // 6
	_ = s.Put(context.Background(), "b", bytes.NewReader([]byte("123456"))) // 6 -> evict a
	if s.bytes > 10 {
		t.Fatalf("cache bytes = %d, want <= 10", s.bytes)
	}
	if _, ok := s.entries["a"]; ok {
		t.Fatal("expected 'a' evicted")
	}
}

func TestObjectsOverMaxObjectBytesNotCached(t *testing.T) {
	inner := newCountingStore()
	s := New(inner, 1<<20, 4).(*Store)
	_ = s.Put(context.Background(), "big", bytes.NewReader([]byte("toolong")))
	if _, ok := s.entries["big"]; ok {
		t.Fatal("oversized object should not be cached")
	}
	read(t, s, "big", 0, 0) // must fall through to inner
	if inner.gets != 1 {
		t.Fatalf("inner gets = %d, want 1", inner.gets)
	}
}

func TestDeleteEvicts(t *testing.T) {
	inner := newCountingStore()
	s := New(inner, 1<<20, 1<<20).(*Store)
	_ = s.Put(context.Background(), "k", bytes.NewReader([]byte("v")))
	if err := s.Delete(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.entries["k"]; ok {
		t.Fatal("expected key evicted after Delete")
	}
}

func TestDisabledWhenMaxBytesZero(t *testing.T) {
	inner := newCountingStore()
	s := New(inner, 0, 0)
	if s != ObjectStore(inner) {
		t.Fatal("expected inner store returned unwrapped when maxBytes<=0")
	}
}
