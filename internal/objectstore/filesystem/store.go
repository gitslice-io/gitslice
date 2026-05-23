package filesystem

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("object store root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	target, err := s.pathForKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, readerWithContext{ctx: ctx, r: r}); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, target)
}

func (s *Store) Get(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	target, err := s.pathForKey(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if length > 0 {
		return struct {
			io.Reader
			io.Closer
		}{Reader: io.LimitReader(f, length), Closer: f}, nil
	}
	return f, nil
}

func (s *Store) Delete(_ context.Context, key string) error {
	target, err := s.pathForKey(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func BlobKey(contentHash string) string {
	hash := strings.TrimPrefix(contentHash, "sha256:")
	if len(hash) >= 4 {
		return filepath.ToSlash(filepath.Join("blobs", "sha256", hash[:2], hash[2:4], hash))
	}
	return filepath.ToSlash(filepath.Join("blobs", "sha256", hash))
}

func (s *Store) pathForKey(key string) (string, error) {
	key = filepath.Clean(strings.TrimPrefix(key, "/"))
	if key == "." || strings.HasPrefix(key, "..") || filepath.IsAbs(key) {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	return filepath.Join(s.root, key), nil
}

type readerWithContext struct {
	ctx context.Context
	r   io.Reader
}

func (r readerWithContext) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}
