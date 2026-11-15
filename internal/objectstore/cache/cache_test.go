package cache

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
)

type countingStore struct {
	mu         sync.Mutex
	objects    map[string][]byte
	gets       int
	puts       int
	copies     int
	deletes    int
	lastPutSrc io.Reader
}

func newCountingStore() *countingStore {
	return &countingStore{objects: map[string][]byte{}}
}

func (c *countingStore) Put(_ context.Context, key string, r io.Reader) error {
	c.mu.Lock()
	c.lastPutSrc = r
	c.mu.Unlock()

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

func (c *countingStore) Copy(_ context.Context, dstKey, srcKey string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.objects[srcKey]
	if !ok {
		return io.EOF
	}
	c.copies++
	c.objects[dstKey] = append([]byte(nil), data...)
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

func TestFullReadMissFillsCache(t *testing.T) {
	inner := newCountingStore()
	_ = inner.Put(context.Background(), "k", bytes.NewReader([]byte("abc")))
	innerGetsBefore := inner.gets
	s := New(inner, 1<<20, 1<<20)
	if got := read(t, s, "k", 0, 0); string(got) != "abc" {
		t.Fatalf("first read = %q", got)
	}
	if got := read(t, s, "k", 0, 0); string(got) != "abc" { // served from cache
		t.Fatalf("second read = %q", got)
	}
	if got := read(t, s, "k", 1, 1); string(got) != "b" { // ranged read of a cached object
		t.Fatalf("ranged read = %q", got)
	}
	if inner.gets-innerGetsBefore != 1 {
		t.Fatalf("inner gets delta = %d, want 1 (the miss fills the cache)", inner.gets-innerGetsBefore)
	}
}

func TestRangedMissDoesNotFillCache(t *testing.T) {
	inner := newCountingStore()
	_ = inner.Put(context.Background(), "k", bytes.NewReader([]byte("abcdef")))
	innerGetsBefore := inner.gets
	s := New(inner, 1<<20, 1<<20)
	if got := read(t, s, "k", 2, 2); string(got) != "cd" {
		t.Fatalf("ranged read = %q", got)
	}
	read(t, s, "k", 0, 0)
	if inner.gets-innerGetsBefore != 2 {
		t.Fatalf("inner gets delta = %d, want 2 (a ranged miss must not fill with partial data)", inner.gets-innerGetsBefore)
	}
}

func TestOversizedMissStreamsWithoutCaching(t *testing.T) {
	inner := newCountingStore()
	big := bytes.Repeat([]byte("x"), 64)
	_ = inner.Put(context.Background(), "big", bytes.NewReader(big))
	innerGetsBefore := inner.gets
	s := New(inner, 1<<20, 16)
	if got := read(t, s, "big", 0, 0); !bytes.Equal(got, big) {
		t.Fatalf("oversized read returned %d bytes, want %d", len(got), len(big))
	}
	read(t, s, "big", 0, 0)
	if inner.gets-innerGetsBefore != 2 {
		t.Fatalf("inner gets delta = %d, want 2 (objects over maxObjectBytes are not cached)", inner.gets-innerGetsBefore)
	}
}

func TestAbandonedReadDoesNotFillCache(t *testing.T) {
	inner := newCountingStore()
	_ = inner.Put(context.Background(), "k", bytes.NewReader([]byte("abcdef")))
	innerGetsBefore := inner.gets
	s := New(inner, 1<<20, 1<<20)
	rc, err := s.Get(context.Background(), "k", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := rc.Read(buf); err != nil {
		t.Fatal(err)
	}
	_ = rc.Close() // closed before EOF: the object was never fully seen
	if got := read(t, s, "k", 0, 0); string(got) != "abcdef" {
		t.Fatalf("read after abandoned read = %q", got)
	}
	if inner.gets-innerGetsBefore != 2 {
		t.Fatalf("inner gets delta = %d, want 2 (a truncated read must not be cached)", inner.gets-innerGetsBefore)
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
	rs := bytes.NewReader([]byte("toolong"))
	if err := s.Put(context.Background(), "big", rs); err != nil {
		t.Fatal(err)
	}
	// Oversized ReadSeeker should be passed directly through without re-wrapping.
	if inner.lastPutSrc != rs {
		t.Fatalf("expected oversized ReadSeeker passed directly to inner.Put, got %T", inner.lastPutSrc)
	}
	if _, ok := s.entries["big"]; ok {
		t.Fatal("oversized object should not be cached")
	}
	read(t, s, "big", 0, 0) // must fall through to inner
	if inner.gets != 1 {
		t.Fatalf("inner gets = %d, want 1", inner.gets)
	}
}

type streamingStepStore struct {
	sawBeforeFinish bool
	received        []byte
}

func (s *streamingStepStore) Put(_ context.Context, _ string, r io.Reader) error {
	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if n > 0 {
		s.sawBeforeFinish = true
	}
	if err != nil && err != io.EOF {
		return err
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.received = append(append([]byte(nil), buf[:n]...), rest...)
	return nil
}

func (s *streamingStepStore) Get(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}

func (s *streamingStepStore) Delete(context.Context, string) error { return nil }

func TestOversizedUnseekablePutStreamsWithoutBufferingAll(t *testing.T) {
	inner := &streamingStepStore{}
	s := New(inner, 1<<20, 4).(*Store)

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		// Write 6 bytes (> maxObjectBytes=4) and wait for inner.Put to start reading
		// BEFORE writing the tail; a full io.ReadAll in cache.Put would deadlock here.
		_, _ = pw.Write([]byte("123456"))
		_, _ = pw.Write([]byte("7890"))
		done <- pw.Close()
	}()

	if err := s.Put(context.Background(), "stream", pr); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("pipe close error = %v", err)
	}
	if !inner.sawBeforeFinish {
		t.Fatal("expected inner.Put to receive streamed bytes before pipe closed")
	}
	if string(inner.received) != "1234567890" {
		t.Fatalf("received = %q, want 1234567890", inner.received)
	}
	if _, ok := s.entries["stream"]; ok {
		t.Fatal("oversized streamed object must not be cached")
	}
}

func TestCopyDelegatesToInnerAndUpdatesCache(t *testing.T) {
	inner := newCountingStore()
	s := New(inner, 1<<20, 1<<20).(*Store)
	if err := s.Put(context.Background(), "staging/1", bytes.NewReader([]byte("payload"))); err != nil {
		t.Fatal(err)
	}
	if err := s.Copy(context.Background(), "blobs/1", "staging/1"); err != nil {
		t.Fatal(err)
	}
	if inner.copies != 1 {
		t.Fatalf("inner copies = %d, want 1", inner.copies)
	}
	if got := read(t, s, "blobs/1", 0, 0); string(got) != "payload" {
		t.Fatalf("got %q, want payload", got)
	}
	if inner.gets != 0 {
		t.Fatalf("inner gets = %d, want 0 (served from cache)", inner.gets)
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
