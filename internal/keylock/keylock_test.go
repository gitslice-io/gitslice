package keylock

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockerSerializesPerKeyAndEvictsIdleEntries(t *testing.T) {
	var locker Locker

	var active atomic.Int32
	var maxActive atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locker.Lock("same-key")
			defer unlock()
			cur := active.Add(1)
			for {
				prev := maxActive.Load()
				if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
		}()
	}
	wg.Wait()
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent holders for same key = %d, want 1", got)
	}
	if got := locker.Len(); got != 0 {
		t.Fatalf("idle locker Len = %d, want 0", got)
	}

	// Many distinct keys are evicted once unlocked, and idempotent unlock is safe.
	for i := 0; i < 100; i++ {
		unlock := locker.Lock(fmt.Sprintf("k-%d", i))
		unlock()
		unlock()
	}
	if got := locker.Len(); got != 0 {
		t.Fatalf("after 100 distinct keys Len = %d, want 0", got)
	}

	// Context cancellation while waiting releases its reference cleanly.
	holdUnlock := locker.Lock("held")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := locker.LockContext(ctx, "held"); !errors.Is(err, context.Canceled) {
		t.Fatalf("LockContext canceled err = %v, want context.Canceled", err)
	}
	holdUnlock()
	if got := locker.Len(); got != 0 {
		t.Fatalf("after canceled waiter + unlock Len = %d, want 0", got)
	}
}
