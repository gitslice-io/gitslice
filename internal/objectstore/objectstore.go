// Package objectstore defines the canonical object storage contract and
// content-addressed object key layout shared across server, service, and
// internal domain packages.
package objectstore

import (
	"context"
	"io"
	"path"
	"strings"
)

// Store is the content-addressed object storage contract implemented by
// concrete drivers (r2, filesystem) and wrappers (packed, cache, latency).
type Store interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// BlobKey returns the canonical content-addressed object storage key for a
// blob content hash (with or without the "sha256:" prefix).
func BlobKey(contentHash string) string {
	hash := strings.TrimPrefix(contentHash, "sha256:")
	if len(hash) >= 4 {
		return path.Join("blobs", "sha256", hash[:2], hash[2:4], hash)
	}
	return path.Join("blobs", "sha256", hash)
}
