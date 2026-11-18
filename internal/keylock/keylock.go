package keylock

import (
	"context"
	"sync"
)

type lockEntry struct {
	ch   chan struct{}
	refs int
}

// Locker serializes operations per string key while evicting idle lock entries
// once all holders and waiters release their references. Zero value is ready to use.
type Locker struct {
	mu    sync.Mutex
	locks map[string]*lockEntry
}

// LockContext acquires the lock for key or returns ctx.Err() if ctx is canceled
// while waiting. The returned unlock function is idempotent.
func (l *Locker) LockContext(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*lockEntry{}
	}
	entry, ok := l.locks[key]
	if !ok {
		entry = &lockEntry{ch: make(chan struct{}, 1)}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	releaseRef := func() {
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}

	select {
	case entry.ch <- struct{}{}:
		return sync.OnceFunc(func() {
			<-entry.ch
			releaseRef()
		}), nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

// Lock acquires the lock for key without context cancellation.
func (l *Locker) Lock(key string) func() {
	unlock, _ := l.LockContext(context.Background(), key)
	return unlock
}

// Len returns the number of currently active or waited-on keys.
func (l *Locker) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}
