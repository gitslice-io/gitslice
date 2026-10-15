package memory

import (
	"bytes"
	"context"
	"io"

	"gitslice.io/gitslice/internal/storage"
)

func (s *ObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *ObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	data, ok := s.b.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	if offset < 0 || length < 0 {
		return nil, storage.ErrInvalid
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	end := int64(len(data))
	if length > 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data[offset:end]...))), nil
}

func (s *ObjectStore) Delete(ctx context.Context, key string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.objects, key)
	return nil
}
