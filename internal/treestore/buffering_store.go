package treestore

import (
	"bytes"
	"context"
	"io"
	"sync"

	"golang.org/x/sync/errgroup"
)

const treeFlushConcurrency = 16

type bufferingObjectStore struct {
	base   ObjectStore
	mu     sync.Mutex
	writes map[string][]byte
}

func newBufferingObjectStore(base ObjectStore) *bufferingObjectStore {
	return &bufferingObjectStore{
		base:   base,
		writes: map[string][]byte{},
	}
}

func (b *bufferingObjectStore) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes[key] = data
	return nil
}

func (b *bufferingObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	b.mu.Lock()
	data, ok := b.writes[key]
	b.mu.Unlock()
	if !ok {
		return b.base.Get(ctx, key, offset, length)
	}
	start := offset
	if start < 0 {
		start = 0
	}
	if start > int64(len(data)) {
		start = int64(len(data))
	}
	end := int64(len(data))
	if length > 0 && start+length < end {
		end = start + length
	}
	return io.NopCloser(bytes.NewReader(data[int(start):int(end)])), nil
}

func (b *bufferingObjectStore) flush(ctx context.Context) error {
	b.mu.Lock()
	writes := make(map[string][]byte, len(b.writes))
	for key, data := range b.writes {
		writes[key] = data
	}
	b.mu.Unlock()
	if len(writes) == 0 {
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(treeFlushConcurrency)
	for key, data := range writes {
		key, data := key, data
		group.Go(func() error {
			return b.base.Put(groupCtx, key, bytes.NewReader(data))
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	b.mu.Lock()
	b.writes = map[string][]byte{}
	b.mu.Unlock()
	return nil
}
