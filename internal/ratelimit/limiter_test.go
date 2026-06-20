package ratelimit

import (
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

func TestLimiterConcurrentUse(t *testing.T) {
	limiter := New(1000, 1000, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				limiter.Allow("alice")
			}
		}()
	}
	wg.Wait()
}
