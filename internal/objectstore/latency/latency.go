// Package latency provides an artificial object-store delay for local
// benchmarks. It is a measurement aid, not a wrapper for production request
// paths.
package latency

import (
	"context"
	"io"
	"time"
)

// ObjectStore is the storage contract wrapped by Store.
type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

// Store delays each operation before forwarding it to the base object store.
type Store struct {
	base  ObjectStore
	delay time.Duration
}

// New returns an object store that delays operations by d. Non-positive delays
// pass operations through without waiting.
func New(base ObjectStore, d time.Duration) *Store {
	return &Store{base: base, delay: d}
}

// Put waits for the configured delay, then stores an object.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.base.Put(ctx, key, r)
}

// Get waits for the configured delay, then retrieves an object.
func (s *Store) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.base.Get(ctx, key, offset, length)
}

// Delete waits for the configured delay, then deletes an object.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.base.Delete(ctx, key)
}

func (s *Store) wait(ctx context.Context) error {
	if s.delay <= 0 {
		return nil
	}
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
