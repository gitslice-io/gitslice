// Package packed wraps an ObjectStore with packfile-style batched writes and
// transparent range-read decompression backed by an external index.
//
// Callers continue to address every blob and tree node by its normal content
// key (e.g. "blobs/sha256:..." or "trees/sha256:..."). Bulk writers (such as
// UploadBlobs and treestore.ApplyEditChain) can call PutPack to bundle many
// immutable objects into a single zstd-compressed pack object in the backing
// store and record their byte ranges in Index. Subsequent Get calls resolve
// packed keys via HTTP range reads against the pack object and fall back to
// loose objects for keys not present in the index.
package packed

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/klauspost/compress/zstd"
)

const (
	CodecRaw  = "raw"
	CodecZstd = "zstd"

	defaultLocCacheSize = 131072
	minZstdCompressSize = 64
)

// ObjectStore is the minimal object storage contract wrapped by Store.
type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type objectCopier interface {
	Copy(ctx context.Context, dstKey, srcKey string) error
}

// Location locates a single object inside a pack object.
type Location struct {
	PackKey   string
	Offset    int64
	Length    int64
	Codec     string
	RawLength int64
}

// IndexedEntry is one object recorded in a pack.
type IndexedEntry struct {
	ObjectKey string
	Offset    int64
	Length    int64
	Codec     string
	RawLength int64
}

// Index maps logical object keys to their pack byte ranges.
type Index interface {
	Lookup(ctx context.Context, objectKey string) (Location, bool, error)
	RecordPack(ctx context.Context, packKey string, entries []IndexedEntry) error
	DeleteKey(ctx context.Context, objectKey string) error
}

type cachedLoc struct {
	packed bool
	loc    Location
}

// Store wraps base with pack-aware PutPack and transparent Get resolution.
type Store struct {
	base     ObjectStore
	index    Index
	locCache *lru.Cache[string, cachedLoc]

	encPool sync.Pool
	decPool sync.Pool
}

// New wraps base with packed-object support backed by index. If index is nil,
// base is returned directly without wrapping.
func New(base ObjectStore, index Index) ObjectStore {
	if index == nil {
		return base
	}
	cache, _ := lru.New[string, cachedLoc](defaultLocCacheSize)
	return &Store{
		base:     base,
		index:    index,
		locCache: cache,
	}
}

func isPackableKey(key string) bool {
	return strings.HasPrefix(key, "blobs/") || strings.HasPrefix(key, "trees/")
}

func (s *Store) getEncoder() (*zstd.Encoder, error) {
	if v := s.encPool.Get(); v != nil {
		return v.(*zstd.Encoder), nil
	}
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
}

func (s *Store) putEncoder(enc *zstd.Encoder) {
	if enc != nil {
		s.encPool.Put(enc)
	}
}

func (s *Store) getDecoder() (*zstd.Decoder, error) {
	if v := s.decPool.Get(); v != nil {
		return v.(*zstd.Decoder), nil
	}
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
}

func (s *Store) putDecoder(dec *zstd.Decoder) {
	if dec != nil {
		s.decPool.Put(dec)
	}
}

// Put writes a single loose object directly to the backing store.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	if err := s.base.Put(ctx, key, r); err != nil {
		return err
	}
	if isPackableKey(key) && s.locCache != nil {
		s.locCache.Add(key, cachedLoc{packed: false})
	}
	return nil
}

// PutPack writes all objects into a single pack at packKey in base, then
// records their byte ranges in index. The pack object is fully durable in base
// before any index row becomes visible.
func (s *Store) PutPack(ctx context.Context, packKey string, objects map[string][]byte) error {
	if len(objects) == 0 {
		return nil
	}
	keys := make([]string, 0, len(objects))
	totalRaw := 0
	for k, v := range objects {
		keys = append(keys, k)
		totalRaw += len(v)
	}
	sort.Strings(keys)

	enc, err := s.getEncoder()
	if err != nil {
		return err
	}
	defer s.putEncoder(enc)

	packBuf := make([]byte, 0, min(totalRaw/2, 8<<20))
	scratch := make([]byte, 0, 64<<10)
	entries := make([]IndexedEntry, 0, len(keys))

	for _, key := range keys {
		raw := objects[key]
		offset := int64(len(packBuf))
		codec := CodecRaw
		payload := raw
		if len(raw) >= minZstdCompressSize {
			compressed := enc.EncodeAll(raw, scratch[:0])
			if len(compressed) < len(raw) {
				codec = CodecZstd
				payload = compressed
			}
		}
		packBuf = append(packBuf, payload...)
		entries = append(entries, IndexedEntry{
			ObjectKey: key,
			Offset:    offset,
			Length:    int64(len(payload)),
			Codec:     codec,
			RawLength: int64(len(raw)),
		})
	}

	if err := s.base.Put(ctx, packKey, bytes.NewReader(packBuf)); err != nil {
		return err
	}
	if err := s.index.RecordPack(ctx, packKey, entries); err != nil {
		return err
	}
	if s.locCache != nil {
		for _, entry := range entries {
			s.locCache.Add(entry.ObjectKey, cachedLoc{
				packed: true,
				loc: Location{
					PackKey:   packKey,
					Offset:    entry.Offset,
					Length:    entry.Length,
					Codec:     entry.Codec,
					RawLength: entry.RawLength,
				},
			})
		}
	}
	return nil
}

// Get retrieves key (or its [offset, offset+length) byte range), transparently
// resolving packed objects via a range read on their pack object and falling
// back to loose objects in base.
func (s *Store) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range offset=%d length=%d", offset, length)
	}
	if !isPackableKey(key) {
		return s.base.Get(ctx, key, offset, length)
	}

	if s.locCache != nil {
		if cached, ok := s.locCache.Get(key); ok {
			if cached.packed {
				return s.getPacked(ctx, cached.loc, offset, length)
			}
			return s.base.Get(ctx, key, offset, length)
		}
	}

	loc, found, err := s.index.Lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	if found {
		if s.locCache != nil {
			s.locCache.Add(key, cachedLoc{packed: true, loc: loc})
		}
		return s.getPacked(ctx, loc, offset, length)
	}

	rc, err := s.base.Get(ctx, key, offset, length)
	if err == nil && s.locCache != nil {
		s.locCache.Add(key, cachedLoc{packed: false})
	}
	return rc, err
}

func (s *Store) getPacked(ctx context.Context, loc Location, offset, length int64) (io.ReadCloser, error) {
	if loc.Codec == CodecRaw || loc.Codec == "" {
		if offset >= loc.RawLength {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		readOff := loc.Offset + offset
		remaining := loc.RawLength - offset
		readLen := remaining
		if length > 0 && length < readLen {
			readLen = length
		}
		if readLen <= 0 {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		return s.base.Get(ctx, loc.PackKey, readOff, readLen)
	}
	if loc.Codec != CodecZstd {
		return nil, fmt.Errorf("unsupported packed object codec %q", loc.Codec)
	}

	rc, err := s.base.Get(ctx, loc.PackKey, loc.Offset, loc.Length)
	if err != nil {
		return nil, err
	}
	compressed, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	dec, err := s.getDecoder()
	if err != nil {
		return nil, err
	}
	raw, err := dec.DecodeAll(compressed, make([]byte, 0, loc.RawLength))
	s.putDecoder(dec)
	if err != nil {
		return nil, fmt.Errorf("decompress packed object from %s: %w", loc.PackKey, err)
	}
	return io.NopCloser(bytes.NewReader(sliceRange(raw, offset, length))), nil
}

func sliceRange(data []byte, offset, length int64) []byte {
	if offset <= 0 && length <= 0 {
		return data
	}
	if offset >= int64(len(data)) {
		return nil
	}
	end := int64(len(data))
	if length > 0 && offset+length < end {
		end = offset + length
	}
	return data[offset:end]
}

// Delete removes key from the loose store and the packed index if present.
func (s *Store) Delete(ctx context.Context, key string) error {
	if isPackableKey(key) {
		if s.locCache != nil {
			s.locCache.Remove(key)
		}
		if err := s.index.DeleteKey(ctx, key); err != nil {
			return err
		}
	}
	return s.base.Delete(ctx, key)
}

// Copy delegates server-side object copies to base when supported.
func (s *Store) Copy(ctx context.Context, dstKey, srcKey string) error {
	if copier, ok := s.base.(objectCopier); ok {
		if err := copier.Copy(ctx, dstKey, srcKey); err != nil {
			return err
		}
		if isPackableKey(dstKey) && s.locCache != nil {
			s.locCache.Add(dstKey, cachedLoc{packed: false})
		}
		return nil
	}
	rc, err := s.Get(ctx, srcKey, 0, 0)
	if err != nil {
		return err
	}
	defer rc.Close()
	return s.Put(ctx, dstKey, rc)
}
