package lru

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheLRUEvictionOrder(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1, time.Time{})
	c.Put("b", 2, time.Time{})

	// Touch "a" so "b" becomes least-recently-used.
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %d, %v; want 1, true", v, ok)
	}

	// Inserting "c" must evict "b", keeping "a".
	c.Put("c", 3, time.Time{})
	if c.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", c.Len())
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected LRU entry b to be evicted")
	}
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) after eviction = %d, %v; want 1, true", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Fatalf("Get(c) = %d, %v; want 3, true", v, ok)
	}

	// Updating an existing key promotes it without evicting the other key.
	c.Put("a", 10, time.Time{})
	c.Put("d", 4, time.Time{})
	if _, ok := c.Get("c"); ok {
		t.Fatal("expected c to be evicted after updating a")
	}
	if v, ok := c.Get("a"); !ok || v != 10 {
		t.Fatalf("Get(a) = %d, %v; want 10, true", v, ok)
	}
}

func TestCacheTTLExpiryAndPurgeBeforeEviction(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := New[string, string](2, WithNow[string](func() time.Time { return now }))

	c.Put("short", "v1", now.Add(5*time.Second))
	c.Put("long", "v2", now.Add(time.Minute))

	if v, ok := c.Get("short"); !ok || v != "v1" {
		t.Fatalf("Get(short) before expiry = %q, %v", v, ok)
	}

	// Advance past short's TTL (exact boundary counts as expired).
	now = now.Add(5 * time.Second)
	// Inserting a third key should purge expired "short" instead of evicting live "long".
	c.Put("fresh", "v3", now.Add(time.Minute))

	if v, ok := c.Get("long"); !ok || v != "v2" {
		t.Fatalf("live entry long was evicted instead of expired short: %q, %v", v, ok)
	}
	if _, ok := c.Get("short"); ok {
		t.Fatal("expired entry short was returned")
	}

	// Inserting an already-expired entry does not store it and removes any prior entry.
	c.Put("fresh", "stale", now)
	if _, ok := c.Get("fresh"); ok {
		t.Fatal("already-expired Put should not be cached")
	}
}

func TestCacheClonePreventsAliasing(t *testing.T) {
	type record struct {
		tags []string
	}
	clone := func(r *record) *record {
		if r == nil {
			return nil
		}
		return &record{tags: slices.Clone(r.tags)}
	}
	c := New[string, *record](4, WithClone(clone))

	src := &record{tags: []string{"a", "b"}}
	c.Put("k", src, time.Time{})
	// Mutating caller's input after Put must not affect cached value.
	src.tags[0] = "mutated-input"

	got1, ok := c.Get("k")
	if !ok || got1.tags[0] != "a" {
		t.Fatalf("Get after input mutation = %+v, %v", got1, ok)
	}
	// Mutating returned value must not affect subsequent Gets.
	got1.tags[1] = "mutated-output"
	got2, ok := c.Get("k")
	if !ok || got2.tags[1] != "b" {
		t.Fatalf("Get after output mutation = %+v, %v", got2, ok)
	}
}

func TestCacheGetOrLoadCoalescingAndClone(t *testing.T) {
	c := New[string, []byte](8, WithClone(slices.Clone[[]byte]))

	var loads atomic.Int32
	gate := make(chan struct{})
	const workers = 16

	var wg sync.WaitGroup
	results := make([][]byte, workers)
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val, err := c.GetOrLoad(context.Background(), "key", "sf:key", func(context.Context) ([]byte, time.Time, bool, error) {
				loads.Add(1)
				<-gate
				return []byte("payload"), time.Time{}, true, nil
			})
			results[idx] = val
			errs[idx] = err
		}(i)
	}

	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := loads.Load(); got != 1 {
		t.Fatalf("concurrent loads = %d, want 1", got)
	}
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d err = %v", i, errs[i])
		}
		if string(results[i]) != "payload" {
			t.Fatalf("worker %d val = %q, want payload", i, results[i])
		}
		// Mutate each worker's slice to ensure no shared backing array.
		results[i][0] = 'X'
	}
	cached, ok := c.Get("key")
	if !ok || string(cached) != "payload" {
		t.Fatalf("cached value mutated by callers: %q, %v", cached, ok)
	}
}

func TestCacheGetOrLoadNonCachedAndErrors(t *testing.T) {
	c := New[string, string](4)

	var uncachedLoads int
	for range 2 {
		v, err := c.GetOrLoad(context.Background(), "opaque", "sf:opaque", func(context.Context) (string, time.Time, bool, error) {
			uncachedLoads++
			return "ephemeral", time.Time{}, false, nil
		})
		if err != nil || v != "ephemeral" {
			t.Fatalf("GetOrLoad(uncached) = %q, %v", v, err)
		}
	}
	if uncachedLoads != 2 {
		t.Fatalf("non-cached loads = %d, want 2", uncachedLoads)
	}
	if _, ok := c.Get("opaque"); ok {
		t.Fatal("non-cached entry was stored in cache")
	}

	wantErr := errors.New("boom")
	var errLoads int
	for range 2 {
		_, err := c.GetOrLoad(context.Background(), "fail", "sf:fail", func(context.Context) (string, time.Time, bool, error) {
			errLoads++
			return "ignored", time.Time{}, true, wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("GetOrLoad(err) = %v, want %v", err, wantErr)
		}
	}
	if errLoads != 2 {
		t.Fatalf("error loads = %d, want 2", errLoads)
	}
	if _, ok := c.Get("fail"); ok {
		t.Fatal("failed load was stored in cache")
	}
}

func TestCacheGetOrLoadContextCancellation(t *testing.T) {
	c := New[string, string](4)

	// Already cancelled context returns immediately without calling load.
	cancelledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	var called atomic.Bool
	if _, err := c.GetOrLoad(cancelledCtx, "k", "sf:k", func(context.Context) (string, time.Time, bool, error) {
		called.Store(true)
		return "v", time.Time{}, true, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled err = %v, want context.Canceled", err)
	}
	if called.Load() {
		t.Fatal("load invoked for already-cancelled context")
	}

	// Cancelling one waiter returns ctx.Err() promptly while a coalesced peer still succeeds.
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int32
	loadFn := func(ctx context.Context) (string, time.Time, bool, error) {
		loads.Add(1)
		close(started)
		<-release
		if ctx.Err() != nil {
			return "", time.Time{}, false, ctx.Err()
		}
		return "shared", time.Time{}, true, nil
	}

	waiter1Ctx, cancel1 := context.WithCancel(context.Background())
	err1Ch := make(chan error, 1)
	go func() {
		_, err := c.GetOrLoad(waiter1Ctx, "shared-key", "sf:shared-key", loadFn)
		err1Ch <- err
	}()
	<-started

	val2Ch := make(chan string, 1)
	err2Ch := make(chan error, 1)
	go func() {
		v, err := c.GetOrLoad(context.Background(), "shared-key", "sf:shared-key", loadFn)
		val2Ch <- v
		err2Ch <- err
	}()
	time.Sleep(15 * time.Millisecond)

	cancel1()
	select {
	case err := <-err1Ch:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not unblock")
	}

	close(release)
	select {
	case err := <-err2Ch:
		if err != nil {
			t.Fatalf("peer waiter err = %v, want nil", err)
		}
		if got := <-val2Ch; got != "shared" {
			t.Fatalf("peer waiter val = %q, want shared", got)
		}
	case <-time.After(time.Second):
		t.Fatal("peer waiter did not complete")
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}
}

func TestCacheZeroCapacityAndDelete(t *testing.T) {
	c := New[string, int](0)
	c.Put("a", 1, time.Time{})
	if _, ok := c.Get("a"); ok {
		t.Fatal("zero-capacity cache should not retain entries")
	}
	v, err := c.GetOrLoad(context.Background(), "a", "sf:a", func(context.Context) (int, time.Time, bool, error) {
		return 42, time.Time{}, true, nil
	})
	if err != nil || v != 42 {
		t.Fatalf("GetOrLoad on zero-capacity cache = %d, %v", v, err)
	}

	c2 := New[string, int](2)
	c2.Put("x", 1, time.Time{})
	c2.Delete("x")
	if _, ok := c2.Get("x"); ok {
		t.Fatal("deleted key still present")
	}
}
