package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLimiterAllowsBurstThenRejects(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newWithClock(10, 2, time.Minute, func() time.Time { return now })

	if !limiter.Allow("alice") {
		t.Fatal("first burst token rejected")
	}
	if !limiter.Allow("alice") {
		t.Fatal("second burst token rejected")
	}
	if limiter.Allow("alice") {
		t.Fatal("request beyond burst allowed")
	}
}

func TestLimiterRefillsAtSteadyRate(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newWithClock(2, 2, time.Minute, func() time.Time { return now })

	if !limiter.Allow("alice") || !limiter.Allow("alice") {
		t.Fatal("initial burst rejected")
	}
	if limiter.Allow("alice") {
		t.Fatal("request beyond initial burst allowed")
	}

	now = now.Add(500 * time.Millisecond)
	if !limiter.Allow("alice") {
		t.Fatal("request after one-token refill rejected")
	}
	if limiter.Allow("alice") {
		t.Fatal("request beyond one-token refill allowed")
	}

	now = now.Add(time.Second)
	if !limiter.Allow("alice") || !limiter.Allow("alice") {
		t.Fatal("refilled burst rejected")
	}
	if limiter.Allow("alice") {
		t.Fatal("request beyond refilled burst allowed")
	}
}

func TestLimiterDisabledWhenRateIsNonPositive(t *testing.T) {
	limiter := New(0, 0, time.Minute)
	for i := 0; i < 100; i++ {
		if !limiter.Allow("alice") {
			t.Fatalf("disabled limiter rejected request %d", i)
		}
	}
}

func TestLimiterIsolatesKeys(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newWithClock(1, 1, time.Minute, func() time.Time { return now })

	if !limiter.Allow("alice") {
		t.Fatal("alice initial request rejected")
	}
	if limiter.Allow("alice") {
		t.Fatal("alice second request allowed")
	}
	if !limiter.Allow("bob") {
		t.Fatal("bob initial request rejected")
	}
}

func TestLimiterEvictsIdleKey(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newWithClock(1, 1, time.Second, func() time.Time { return now })

	if !limiter.Allow("alice") {
		t.Fatal("initial request rejected")
	}
	if limiter.Allow("alice") {
		t.Fatal("second request allowed before idle eviction")
	}

	now = now.Add(1100 * time.Millisecond)
	if !limiter.Allow("alice") {
		t.Fatal("request after idle eviction rejected")
	}
}

func TestLimiterBoundsCardinalityViaLRU(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newWithClockAndMax(1, 1, 10*time.Minute, 3, func() time.Time { return now })

	// Exhaust burst on a, b, c.
	for _, k := range []string{"a", "b", "c"} {
		if !limiter.Allow(k) {
			t.Fatalf("initial Allow(%q) rejected", k)
		}
	}
	// Touch "a" again (rejected, so still 0 tokens, but promoted to MRU).
	if limiter.Allow("a") {
		t.Fatal("second Allow(a) should be rejected")
	}
	// Insert "d" -> should evict LRU ("b"), keeping "a" and "c".
	if !limiter.Allow("d") {
		t.Fatal("initial Allow(d) rejected")
	}
	if got := limiter.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	// "a" and "c" must still be remembered as exhausted.
	if limiter.Allow("a") {
		t.Fatal("MRU key 'a' should not have been evicted")
	}
	if limiter.Allow("c") {
		t.Fatal("middle key 'c' should not have been evicted")
	}

	// Hammer 500 distinct keys and ensure map never grows past maxEntries=3.
	for i := 0; i < 500; i++ {
		limiter.Allow(fmt.Sprintf("bot-%d", i))
		if got := limiter.Len(); got > 3 {
			t.Fatalf("Len() = %d after %d keys, want <= 3", got, i)
		}
	}
}

func TestLimiterPeekDoesNotConsumeTokens(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newWithClock(1, 1, time.Minute, func() time.Time { return now })

	for i := 0; i < 5; i++ {
		if !limiter.Peek("ip-1") {
			t.Fatalf("Peek %d returned false before any Allow", i)
		}
	}
	if limiter.Len() != 0 {
		t.Fatalf("Peek allocated bucket: Len() = %d, want 0", limiter.Len())
	}
	if !limiter.Allow("ip-1") {
		t.Fatal("Allow rejected after Peek")
	}
	if limiter.Peek("ip-1") {
		t.Fatal("Peek returned true after burst exhausted")
	}
	now = now.Add(time.Second)
	if !limiter.Peek("ip-1") {
		t.Fatal("Peek returned false after refill")
	}
}

func TestLimiterConcurrentUse(t *testing.T) {
	limiter := NewBounded(1000, 1000, time.Minute, 16)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				limiter.Allow(fmt.Sprintf("user-%d", (i+j)%32))
				limiter.Peek(fmt.Sprintf("user-%d", j))
			}
		}()
	}
	wg.Wait()
	if got := limiter.Len(); got > 16 {
		t.Fatalf("Len() = %d, want <= 16", got)
	}
}
