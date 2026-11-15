package ratelimit

import (
	"sync"
	"time"
)

// Limiter enforces a per-key token-bucket rate limit with burst capacity.
// Zero or negative ratePerSec disables limiting (Allow always true).
type Limiter struct {
	mu         sync.Mutex
	ratePerSec float64
	burst      int
	ttl        time.Duration
	now        func() time.Time
	lastSweep  time.Time
	buckets    map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter allowing ratePerSec sustained requests per key with
// up to burst accumulated tokens. Idle keys are evicted after ttl to bound
// memory.
func New(ratePerSec float64, burst int, ttl time.Duration) *Limiter {
	return newWithClock(ratePerSec, burst, ttl, time.Now)
}

func newWithClock(ratePerSec float64, burst int, ttl time.Duration, now func() time.Time) *Limiter {
	return &Limiter{
		ratePerSec: ratePerSec,
		burst:      burst,
		ttl:        ttl,
		now:        now,
		buckets:    map[string]*bucket{},
	}
}

// Allow reports whether one request for key is permitted right now, consuming a
// token if so. Safe for concurrent use.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.ratePerSec <= 0 {
		return true
	}

	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	b := l.buckets[key]
	if b == nil || l.idle(now, b) {
		b = &bucket{
			tokens: float64(l.burst),
			last:   now,
		}
		l.buckets[key] = b
	}

	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = minFloat(float64(l.burst), b.tokens+elapsed*l.ratePerSec)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) idle(now time.Time, b *bucket) bool {
	return l.ttl > 0 && now.Sub(b.last) > l.ttl
}

func (l *Limiter) sweep(now time.Time) {
	if l.ttl <= 0 {
		return
	}
	if l.lastSweep.IsZero() {
		l.lastSweep = now
		return
	}
	if now.Sub(l.lastSweep) < l.sweepInterval() {
		return
	}
	for key, b := range l.buckets {
		if l.idle(now, b) {
			delete(l.buckets, key)
		}
	}
	l.lastSweep = now
}

func (l *Limiter) sweepInterval() time.Duration {
	interval := l.ttl / 2
	if interval <= 0 {
		interval = l.ttl
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
