// Package cache wraps an object store with a bounded, write-through in-memory
// cache. All objects in this store are content-addressed and immutable (blobs
// keyed by content hash, tree nodes keyed by tree id), so serving a cached value
// for a key is always correct. The cache is write-through only: Put populates the
// cache, Get serves cached keys without touching the inner store, and reads that
// miss are NOT cached (so large blob streams are never buffered on the read path).
// This collapses the repeated cross-commit / cross-phase tree-node reads a git
// import does against remote object storage.
package cache

import (
	"bytes"
	"container/list"
	"context"
	"fmt"
	"io"
	"math"
	"sync"
)

const defaultMaxObjectBytes int64 = 4 << 20

type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type Store struct {
	inner          ObjectStore
	maxBytes       int64
	maxObjectBytes int64

	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	bytes   int64
}

type entry struct {
	key  string
	data []byte
}

// New wraps inner with a write-through cache bounded to maxBytes total. Objects
// larger than maxObjectBytes are never cached. When maxBytes <= 0 the inner store
// is returned unwrapped (caching disabled).
func New(inner ObjectStore, maxBytes, maxObjectBytes int64) ObjectStore {
	if maxBytes <= 0 {
		return inner
	}
	if maxObjectBytes <= 0 {
		maxObjectBytes = defaultMaxObjectBytes
	}
	return &Store{
		inner:          inner,
		maxBytes:       maxBytes,
		maxObjectBytes: maxObjectBytes,
		entries:        make(map[string]*list.Element),
		lru:            list.New(),
	}
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	if r == nil {
		return fmt.Errorf("object reader is required")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := s.inner.Put(ctx, key, bytes.NewReader(data)); err != nil {
		return err
	}
	if int64(len(data)) > s.maxObjectBytes {
		s.remove(key)
		return nil
	}
	s.insert(key, data)
	return nil
}

func (s *Store) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if data, ok, err := s.cachedRange(key, offset, length); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return s.inner.Get(ctx, key, offset, length)
}

func (s *Store) Delete(ctx context.Context, key string) error {
	err := s.inner.Delete(ctx, key)
	s.remove(key)
	return err
}

func (s *Store) cachedRange(key string, offset, length int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	elem, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	s.lru.MoveToFront(elem)
	data := elem.Value.(*entry).data
	ranged, err := objectRange(data, offset, length)
	if err != nil {
		return nil, true, err
	}
	return ranged, true, nil
}

func (s *Store) insert(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.entries[key]; ok {
		ent := elem.Value.(*entry)
		s.bytes -= int64(len(ent.data))
		ent.data = data
		s.bytes += int64(len(data))
		s.lru.MoveToFront(elem)
	} else {
		elem := s.lru.PushFront(&entry{key: key, data: data})
		s.entries[key] = elem
		s.bytes += int64(len(data))
	}
	s.evictLocked()
}

func (s *Store) remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(key)
}

func (s *Store) removeLocked(key string) {
	elem, ok := s.entries[key]
	if !ok {
		return
	}
	ent := elem.Value.(*entry)
	s.bytes -= int64(len(ent.data))
	delete(s.entries, key)
	s.lru.Remove(elem)
}

func (s *Store) evictLocked() {
	for s.bytes > s.maxBytes {
		elem := s.lru.Back()
		if elem == nil {
			return
		}
		s.removeLocked(elem.Value.(*entry).key)
	}
}

func objectRange(data []byte, offset, length int64) ([]byte, error) {
	if offset < 0 {
		offset = 0
	}
	if length > 0 && offset > math.MaxInt64-length {
		return nil, fmt.Errorf("invalid object range offset %d length %d", offset, length)
	}

	dataLen := int64(len(data))
	if offset > dataLen {
		return data[len(data):], nil
	}
	end := dataLen
	if length > 0 {
		end = offset + length
		if end > dataLen {
			end = dataLen
		}
	}
	return data[int(offset):int(end)], nil
}
