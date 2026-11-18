package mountd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const (
	PrefetchMaxFile  = 64 << 10
	BatchMaxFiles    = 256
	BatchMaxBytes    = 4 << 20
	LargeFileBytes   = 4 << 20
	FetchTimeout     = 60 * time.Second
	FetchConcurrency = 4
	OutageWindow     = 5 * time.Second
)

var (
	FetchRetryBackoff    = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
	ErrServerUnreachable = errors.New("server unreachable")
)

// FetchFunc fetches entries at commitID into the local cache.
type FetchFunc func(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (files int, bytes int64, err error)

// Fetcher fetches content into the client cache for a mount: base files by path
// at the base commit, and content the base does not have at that path by hash.
type Fetcher struct {
	conn    *grpc.ClientConn
	slice   *corev1.SliceRef
	cache   *clientcache.ObjectCache
	fetchFn FetchFunc

	mu        sync.Mutex
	commitID  string
	downUntil time.Time
	manifest  *lazyfs.Manifest
	byHash    map[string]lazyfs.BaseEntry
	inflight  map[string]*fetchCall
	completed map[string]bool
	files     int64
	bytes     int64
	ahead     readAhead
}

type fetchCall struct {
	done chan struct{}
	err  error
}

// NewFetcher constructs a mount content Fetcher.
func NewFetcher(conn *grpc.ClientConn, slice *corev1.SliceRef, cache *clientcache.ObjectCache, manifest *lazyfs.Manifest, fetchFn FetchFunc) *Fetcher {
	commitID := ""
	if manifest != nil {
		commitID = manifest.CommitID()
	}
	return &Fetcher{
		conn:      conn,
		slice:     slice,
		cache:     cache,
		fetchFn:   fetchFn,
		commitID:  commitID,
		manifest:  manifest,
		inflight:  map[string]*fetchCall{},
		completed: map[string]bool{},
	}
}

// SetBase points the fetcher at a new base manifest.
func (f *Fetcher) SetBase(manifest *lazyfs.Manifest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifest, f.commitID, f.byHash, f.completed = manifest, manifest.CommitID(), nil, map[string]bool{}
	f.ahead.reset()
}

// Stats returns cumulative fetched file and byte counts.
func (f *Fetcher) Stats() (files, bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files, f.bytes
}

// AddStats adds background prefetch counts to the fetcher totals.
func (f *Fetcher) AddStats(files int, bytes int64) {
	f.mu.Lock()
	f.files += int64(files)
	f.bytes += bytes
	f.mu.Unlock()
}

// Fetch implements fusefs.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context, p string, attr lazyfs.Attr) (string, bool, error) {
	f.observeRead(p)
	fetched, err := f.FetchContent(ctx, p, attr.ContentHash, attr.Size)
	if err != nil {
		return "", false, err
	}
	path, err := f.cache.Path(attr.ContentHash)
	return path, fetched, err
}

func waitForFetchCall(ctx context.Context, call *fetchCall) error {
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FetchContent ensures content with hash is in the cache, returning whether it
// had to fetch from the server.
func (f *Fetcher) FetchContent(ctx context.Context, p, hash string, size int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.cache.Exists(hash) {
		return false, nil
	}
	f.mu.Lock()
	if call := f.inflight[hash]; call != nil {
		f.mu.Unlock()
		return false, waitForFetchCall(ctx, call)
	}
	if wait := time.Until(f.downUntil); wait > 0 {
		f.mu.Unlock()
		return false, fmt.Errorf("the server is not reachable (trying again in %s): %w", wait.Round(time.Second), ErrServerUnreachable)
	}
	commitID := f.commitID
	primary, siblings := f.candidatesLocked(p, hash, size)
	f.mu.Unlock()

	if f.cache.Exists(hash) {
		return false, nil
	}
	filtered := make([]lazyfs.BaseEntry, 0, min(len(siblings)+1, BatchMaxFiles))
	filtered = append(filtered, primary)
	total := primary.Size
	for _, sibling := range siblings {
		if len(filtered) >= BatchMaxFiles || total+sibling.Size > BatchMaxBytes {
			break
		}
		if f.cache.Exists(sibling.ContentHash) {
			continue
		}
		filtered = append(filtered, sibling)
		total += sibling.Size
	}

	f.mu.Lock()
	if call := f.inflight[hash]; call != nil {
		f.mu.Unlock()
		return false, waitForFetchCall(ctx, call)
	}
	if f.completed[hash] && f.cache.Exists(hash) {
		f.mu.Unlock()
		return false, nil
	}
	if wait := time.Until(f.downUntil); wait > 0 {
		f.mu.Unlock()
		return false, fmt.Errorf("the server is not reachable (trying again in %s): %w", wait.Round(time.Second), ErrServerUnreachable)
	}
	call := &fetchCall{done: make(chan struct{})}
	batch := make([]lazyfs.BaseEntry, 0, len(filtered))
	for i, entry := range filtered {
		if i > 0 && (f.inflight[entry.ContentHash] != nil || f.completed[entry.ContentHash]) {
			continue
		}
		f.inflight[entry.ContentHash] = call
		batch = append(batch, entry)
	}
	f.mu.Unlock()

	go func() {
		_ = f.runBatch(commitID, batch, call)
	}()
	if err := waitForFetchCall(ctx, call); err != nil {
		return false, err
	}
	return true, nil
}

func (f *Fetcher) runBatch(commitID string, batch []lazyfs.BaseEntry, call *fetchCall) error {
	fetchCtx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
	defer cancel()
	var files int
	var bytes int64
	err := RetryUnavailable(fetchCtx, FetchRetryBackoff, func() error {
		n, size, err := f.fetch(fetchCtx, commitID, batch)
		files += n
		bytes += size
		return err
	})

	f.mu.Lock()
	if err == nil {
		if f.completed == nil || len(f.completed) > 65536 {
			f.completed = map[string]bool{}
		}
		for _, entry := range batch {
			f.completed[entry.ContentHash] = true
		}
	}
	for _, entry := range batch {
		delete(f.inflight, entry.ContentHash)
	}
	f.files += int64(files)
	f.bytes += bytes
	if grpcstatus.Code(err) == codes.Unavailable {
		f.downUntil = time.Now().Add(OutageWindow)
	}
	f.mu.Unlock()
	call.err = err
	close(call.done)
	return err
}

// RetryUnavailable retries fn on gRPC Unavailable errors with pauses.
func RetryUnavailable(ctx context.Context, pauses []time.Duration, fn func() error) error {
	err := fn()
	for _, pause := range pauses {
		if err == nil || grpcstatus.Code(err) != codes.Unavailable || ctx.Err() != nil {
			return err
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		err = fn()
	}
	return err
}

func (f *Fetcher) candidatesLocked(p, hash string, size int64) (lazyfs.BaseEntry, []lazyfs.BaseEntry) {
	if f.manifest == nil {
		return lazyfs.BaseEntry{Kind: lazyfs.KindFile, Size: size, ContentHash: hash}, nil
	}
	entry, ok := f.manifest.Lookup(p)
	if !ok || entry.Kind != lazyfs.KindFile || entry.ContentHash != hash {
		if f.byHash == nil {
			f.byHash = map[string]lazyfs.BaseEntry{}
			f.manifest.Files(func(entry lazyfs.BaseEntry) {
				if entry.Kind == lazyfs.KindFile {
					f.byHash[entry.ContentHash] = entry
				}
			})
		}
		if other, ok := f.byHash[hash]; ok {
			return other, nil
		}
		return lazyfs.BaseEntry{Kind: lazyfs.KindFile, Size: size, ContentHash: hash}, nil
	}
	if entry.Size > LargeFileBytes {
		return entry, nil
	}
	seen := map[string]bool{hash: true}
	dir := p[:strings.LastIndex(p, "/")]
	if dir == "" {
		dir = "/"
	}
	var siblings []lazyfs.BaseEntry
	for _, name := range f.manifest.Children(dir) {
		sibling, ok := f.manifest.Lookup(joinGlobalPath(dir, name))
		if !ok || sibling.Kind != lazyfs.KindFile || sibling.Size > PrefetchMaxFile || seen[sibling.ContentHash] {
			continue
		}
		if f.inflight[sibling.ContentHash] != nil || f.completed[sibling.ContentHash] {
			continue
		}
		seen[sibling.ContentHash] = true
		siblings = append(siblings, sibling)
	}
	return entry, siblings
}

func joinGlobalPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

func (f *Fetcher) fetch(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error) {
	if f.fetchFn != nil {
		return f.fetchFn(ctx, commitID, entries)
	}
	return 0, 0, errors.New("no mount fetch function configured")
}

// CacheContent implements lazyfs.Content backed by an ObjectCache.
type CacheContent struct {
	Cache *clientcache.ObjectCache
}

func (c CacheContent) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	file, err := c.Cache.Open(hash)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("content %s is not in the client cache: %w", hash, err)
	}
	return file, err
}

func (c CacheContent) HashFile(p string) (string, error) {
	object, err := c.Cache.PutFile(p)
	return object.ContentHash, err
}

func (c CacheContent) HashBytes(data []byte) (string, error) {
	object, err := c.Cache.PutBytes(data)
	return object.ContentHash, err
}

// Content wraps CacheContent to fetch missing blobs via Fetcher on first read.
type Content struct {
	CacheContent
	Fetcher *Fetcher
}

func (c Content) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	if _, err := c.Fetcher.FetchContent(ctx, "", hash, 0); err != nil {
		return nil, err
	}
	return c.CacheContent.Open(ctx, hash)
}

// FetchConcurrently splits entries across FetchConcurrency workers calling fetchGroup.
func FetchConcurrently(ctx context.Context, entries []lazyfs.BaseEntry, fetchGroup func(ctx context.Context, group []lazyfs.BaseEntry) (int, int64, error)) (int, int64, error) {
	groups := make([][]lazyfs.BaseEntry, FetchConcurrency)
	for i, entry := range entries {
		groups[i%len(groups)] = append(groups[i%len(groups)], entry)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	files, total := 0, int64(0)
	var firstErr error
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		wg.Add(1)
		go func(group []lazyfs.BaseEntry) {
			defer wg.Done()
			n, size, err := fetchGroup(ctx, group)
			mu.Lock()
			defer mu.Unlock()
			files += n
			total += size
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}(group)
	}
	wg.Wait()
	return files, total, firstErr
}
