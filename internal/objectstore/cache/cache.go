// Package cache wraps an object store with a bounded in-memory cache. All
// objects in this store are content-addressed and immutable (blobs keyed by
// content hash, tree nodes keyed by tree id), so serving a cached value for a
// key is always correct.
//
// Put populates the cache (write-through), and Get serves cached keys without
// touching the inner store. A full-object Get that misses also fills the cache
// once the caller has read the object to EOF, as long as it fits within
// maxObjectBytes. Larger objects and partial (ranged) reads stream through
// without being cached, and nothing beyond maxObjectBytes is ever buffered.
//
// Read-fill matters for serving: an instance that did not write an object
// (every instance after a restart or scale-from-zero) would otherwise fetch the
// same tree nodes from remote object storage on every request.
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
	rc, err := s.inner.Get(ctx, key, offset, length)
	if err != nil || offset > 0 || length > 0 {
		return rc, err
	}
	return &fillingReader{store: s, key: key, inner: rc}, nil
}

// fillingReader passes a full-object read through and, if the object reaches
// EOF within maxObjectBytes, inserts it into the cache. Once the object grows
// past the limit it stops buffering and the read continues as a plain stream.
type fillingReader struct {
	store    *Store
	key      string
	inner    io.ReadCloser
	buf      []byte
	overflow bool
	done     bool
}

func (r *fillingReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 && !r.overflow {
		if int64(len(r.buf)+n) > r.store.maxObjectBytes {
			r.overflow = true
			r.buf = nil
		} else {
			r.buf = append(r.buf, p[:n]...)
		}
	}
	if err == io.EOF && !r.overflow && !r.done {
		r.done = true
		data := r.buf
		if data == nil {
			data = []byte{}
		}
		r.store.insert(r.key, data)
		r.buf = nil
	}
	return n, err
}

func (r *fillingReader) Close() error {
	r.buf = nil
	return r.inner.Close()
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
