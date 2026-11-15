package ratelimit

import (
	"sync"
	"time"
)

// DefaultMaxEntries bounds the number of tracked keys in a Limiter so high
// cardinality traffic (for example botnet IP churn) cannot grow memory without
// bound between TTL sweeps.
const DefaultMaxEntries = 65536

// Limiter enforces a per-key token-bucket rate limit with burst capacity and
// bounded LRU key tracking. Zero or negative ratePerSec disables limiting
// (Allow always true).
type Limiter struct {
	mu         sync.Mutex
	ratePerSec float64
	burst      int
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	lastSweep  time.Time
	buckets    map[string]*bucket
	head       *bucket // most recently used
	tail       *bucket // least recently used
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
	prev   *bucket
	next   *bucket
}

// New returns a limiter allowing ratePerSec sustained requests per key with
// up to burst accumulated tokens. Idle keys are evicted after ttl and total
// tracked keys are capped at DefaultMaxEntries via LRU eviction.
func New(ratePerSec float64, burst int, ttl time.Duration) *Limiter {
	return newWithClockAndMax(ratePerSec, burst, ttl, DefaultMaxEntries, time.Now)
}

// NewBounded is like New with an explicit maximum key cardinality cap. When
// maxEntries <= 0, DefaultMaxEntries is used.
func NewBounded(ratePerSec float64, burst int, ttl time.Duration, maxEntries int) *Limiter {
	return newWithClockAndMax(ratePerSec, burst, ttl, maxEntries, time.Now)
}

func newWithClock(ratePerSec float64, burst int, ttl time.Duration, now func() time.Time) *Limiter {
	return newWithClockAndMax(ratePerSec, burst, ttl, DefaultMaxEntries, now)
}

func newWithClockAndMax(ratePerSec float64, burst int, ttl time.Duration, maxEntries int, now func() time.Time) *Limiter {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	return &Limiter{
		ratePerSec: ratePerSec,
		burst:      burst,
		ttl:        ttl,
		maxEntries: maxEntries,
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

	l.evictExpiredTail(now)
	l.sweep(now)

	b := l.buckets[key]
	switch {
	case b == nil:
		if len(l.buckets) >= l.maxEntries && l.tail != nil {
			victim := l.tail
			l.removeNode(victim)
			delete(l.buckets, victim.key)
		}
		b = &bucket{
			key:    key,
			tokens: float64(l.burst),
			last:   now,
		}
		l.buckets[key] = b
		l.pushFront(b)
	case l.idle(now, b):
		b.tokens = float64(l.burst)
		b.last = now
		l.moveToFront(b)
	default:
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens = minFloat(float64(l.burst), b.tokens+elapsed*l.ratePerSec)
		}
		b.last = now
		l.moveToFront(b)
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Peek reports whether key currently has at least one available token without
// consuming a token or allocating a bucket. Useful for failure-only limiters
// that check quota up front and charge via Allow only when a request fails.
func (l *Limiter) Peek(key string) bool {
	if l == nil || l.ratePerSec <= 0 {
		return true
	}

	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil || l.idle(now, b) {
		return l.burst >= 1
	}
	tokens := b.tokens
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		tokens = minFloat(float64(l.burst), tokens+elapsed*l.ratePerSec)
	}
	return tokens >= 1
}

// Len returns the current number of tracked keys (primarily for tests).
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *Limiter) pushFront(b *bucket) {
	b.prev = nil
	b.next = l.head
	if l.head != nil {
		l.head.prev = b
	} else {
		l.tail = b
	}
	l.head = b
}

func (l *Limiter) removeNode(b *bucket) {
	if b.prev != nil {
		b.prev.next = b.next
	} else {
		l.head = b.next
	}
	if b.next != nil {
		b.next.prev = b.prev
	} else {
		l.tail = b.prev
	}
	b.prev = nil
	b.next = nil
}

func (l *Limiter) moveToFront(b *bucket) {
	if l.head == b {
		return
	}
	l.removeNode(b)
	l.pushFront(b)
}

func (l *Limiter) evictExpiredTail(now time.Time) {
	if l.ttl <= 0 {
		return
	}
	for l.tail != nil && l.idle(now, l.tail) {
		victim := l.tail
		l.removeNode(victim)
		delete(l.buckets, victim.key)
	}
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
			l.removeNode(b)
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
