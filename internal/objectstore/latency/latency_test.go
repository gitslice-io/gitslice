package latency

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestStoreDelaysEachOperation(t *testing.T) {
	const delay = 20 * time.Millisecond
	base := newMemoryStore()
	store := New(base, delay)

	assertDelayed(t, delay, func() error {
		return store.Put(context.Background(), "object", bytes.NewBufferString("value"))
	})
	assertDelayed(t, delay, func() error {
		r, err := store.Get(context.Background(), "object", 0, 0)
		if err != nil {
			return err
		}
		return r.Close()
	})
	assertDelayed(t, delay, func() error {
		return store.Delete(context.Background(), "object")
	})
}

func TestStoreCancelledContextReturnsPromptly(t *testing.T) {
	const delay = 200 * time.Millisecond
	base := newMemoryStore()
	store := New(base, delay)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "put",
			call: func() error {
				return store.Put(ctx, "object", bytes.NewBufferString("value"))
			},
		},
		{
			name: "get",
			call: func() error {
				_, err := store.Get(ctx, "object", 0, 0)
				return err
			},
		},
		{
			name: "delete",
			call: func() error {
				return store.Delete(ctx, "object")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start := time.Now()
			err := test.call()
			elapsed := time.Since(start)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("operation error = %v, want context.Canceled", err)
			}
			if elapsed >= delay/2 {
				t.Fatalf("cancelled operation took %s, want less than %s", elapsed, delay/2)
			}
		})
	}
	if calls := base.callCount(); calls != 0 {
		t.Fatalf("base call count = %d, want 0", calls)
	}
}

func assertDelayed(t *testing.T, delay time.Duration, call func() error) {
	t.Helper()
	start := time.Now()
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < delay {
		t.Fatalf("operation took %s, want at least %s", elapsed, delay)
	}
}

type memoryStore struct {
	mu    sync.Mutex
	data  map[string][]byte
	calls int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: make(map[string][]byte)}
}

func (s *memoryStore) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.data[key] = data
	return nil
}

func (s *memoryStore) Get(_ context.Context, key string, _, _ int64) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return io.NopCloser(bytes.NewReader(s.data[key])), nil
}

func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	delete(s.data, key)
	return nil
}

func (s *memoryStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
