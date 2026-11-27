// Package lru provides a small, generic, thread-safe bounded LRU cache with
// optional per-entry TTL, value cloning, and singleflight load coalescing.
package lru

import (
	"container/list"
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type options[V any] struct {
	now   func() time.Time
	clone func(V) V
}

// Option configures a Cache created by New.
type Option[V any] func(*options[V])

// WithNow overrides the clock used for TTL checks (defaults to time.Now).
func WithNow[V any](now func() time.Time) Option[V] {
	return func(o *options[V]) {
		o.now = now
	}
}

// WithClone configures a function called when storing values and when returning
// values from Get or GetOrLoad so callers never alias mutable cached state.
func WithClone[V any](clone func(V) V) Option[V] {
	return func(o *options[V]) {
		o.clone = clone
	}
}

type entry[K comparable, V any] struct {
	key       K
	val       V
	expiresAt time.Time
}

// Cache is a bounded, thread-safe LRU cache with optional per-entry expiry and
// singleflight load coalescing.
type Cache[K comparable, V any] struct {
	maxEntries int
	now        func() time.Time
	clone      func(V) V

	mu      sync.Mutex
	entries map[K]*list.Element
	lru     *list.List
	sf      singleflight.Group
}

// New creates a Cache holding at most maxEntries items. A non-positive
// maxEntries creates a zero-capacity cache that stores nothing (GetOrLoad still
// coalesces concurrent loads).
func New[K comparable, V any](maxEntries int, opts ...Option[V]) *Cache[K, V] {
	var cfg options[V]
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if maxEntries < 0 {
		maxEntries = 0
	}
	return &Cache[K, V]{
		maxEntries: maxEntries,
		now:        cfg.now,
		clone:      cfg.clone,
		entries:    make(map[K]*list.Element),
		lru:        list.New(),
	}
}

// Get returns the cached value for key if present and not expired, promoting
// the entry to most-recently-used.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	e := elem.Value.(*entry[K, V])
	if isExpired(e.expiresAt, c.now()) {
		c.lru.Remove(elem)
		delete(c.entries, key)
		var zero V
		return zero, false
	}
	c.lru.MoveToFront(elem)
	return c.cloneVal(e.val), true
}

// Put inserts or updates key with val and optional expiresAt (zero means no
// expiry). If expiresAt is non-zero and already elapsed, any existing entry
// for key is removed and nothing is stored.
func (c *Cache[K, V]) Put(key K, val V, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.maxEntries == 0 {
		return
	}
	at := c.now()
	if isExpired(expiresAt, at) {
		if elem, ok := c.entries[key]; ok {
			c.lru.Remove(elem)
			delete(c.entries, key)
		}
		return
	}

	stored := c.cloneVal(val)
	if elem, ok := c.entries[key]; ok {
		e := elem.Value.(*entry[K, V])
		e.val = stored
		e.expiresAt = expiresAt
		c.lru.MoveToFront(elem)
		return
	}

	if len(c.entries) >= c.maxEntries {
		for k, elem := range c.entries {
			if isExpired(elem.Value.(*entry[K, V]).expiresAt, at) {
				c.lru.Remove(elem)
				delete(c.entries, k)
			}
		}
		for len(c.entries) >= c.maxEntries {
			oldest := c.lru.Back()
			if oldest == nil {
				break
			}
			evicted := oldest.Value.(*entry[K, V])
			c.lru.Remove(oldest)
			delete(c.entries, evicted.key)
		}
	}

	c.entries[key] = c.lru.PushFront(&entry[K, V]{
		key:       key,
		val:       stored,
		expiresAt: expiresAt,
	})
}

// Delete removes key from the cache if present.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		c.lru.Remove(elem)
		delete(c.entries, key)
	}
}

// Len returns the number of entries currently held in the cache (including any
// lazily expired entries not yet evicted).
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// GetOrLoad returns the cached value for key or invokes load, coalescing
// concurrent misses sharing sfKey via singleflight.
//
// load returns (value, expiresAt, cacheable, err). Errors are never cached.
// When cacheable is true and err is nil, the value is stored with expiresAt
// (zero means no expiry). When cacheable is false, the value is returned to
// waiting callers without being stored.
//
// Waiters observe ctx.Done() while a coalesced load is in flight; load itself
// receives context.WithoutCancel(ctx) so one cancelled caller does not abort
// the shared load for coalesced peers.
func (c *Cache[K, V]) GetOrLoad(
	ctx context.Context,
	key K,
	sfKey string,
	load func(ctx context.Context) (V, time.Time, bool, error),
) (V, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		var zero V
		return zero, err
	}
	if val, ok := c.Get(key); ok {
		return val, nil
	}

	loadCtx := context.WithoutCancel(ctx)
	ch := c.sf.DoChan(sfKey, func() (any, error) {
		if val, ok := c.Get(key); ok {
			return val, nil
		}
		val, expiresAt, cacheable, err := load(loadCtx)
		if err != nil {
			return nil, err
		}
		if cacheable {
			c.Put(key, val, expiresAt)
		}
		return c.cloneVal(val), nil
	})

	select {
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			var zero V
			return zero, res.Err
		}
		val, _ := res.Val.(V)
		return c.cloneVal(val), nil
	}
}

func (c *Cache[K, V]) cloneVal(v V) V {
	if c.clone != nil {
		return c.clone(v)
	}
	return v
}

func isExpired(expiresAt, at time.Time) bool {
	return !expiresAt.IsZero() && !at.Before(expiresAt)
}
