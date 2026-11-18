package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Fetching. A miss fetches the file and, in the same ReadFiles call, small
// files next to it, as gs init batches (design/28_benchmarks.md): reading one
// file of a directory usually means reading more.
const (
	mountPrefetchMaxFile  = 64 << 10
	mountBatchMaxFiles    = 256
	mountBatchMaxBytes    = 4 << 20
	mountLargeFileBytes   = 4 << 20
	mountFetchTimeout     = 60 * time.Second
	mountFetchConcurrency = 4
)

// mountFetcher fetches content into the client cache for a mount: base files
// by path at the base commit, and content the base does not have at that path
// (a changeset's files after gs switch) by its hash.
type mountFetcher struct {
	r     Runner
	conn  *grpc.ClientConn
	slice *corev1.SliceRef
	cache *clientcache.ObjectCache

	mu       sync.Mutex
	commitID string
	// downUntil is when fetches try the server again after one gave up on an
	// unreachable server; until then they fail at once, so a program reading
	// many uncached files offline is not held up by each one.
	downUntil time.Time
	manifest  *lazyfs.Manifest
	byHash    map[string]lazyfs.BaseEntry // a base file with each content hash, built when first needed
	inflight  map[string]*fetchCall       // by content hash
	completed map[string]bool             // hashes fetched on current base, avoiding duplicate schedules across unlocked Stat
	files     int64
	bytes     int64

	// fetchFn replaces the server fetch in tests.
	fetchFn func(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error)
	ahead   readAhead
}

type fetchCall struct {
	done chan struct{}
	err  error
}

func newMountFetcher(r Runner, conn *grpc.ClientConn, slice *corev1.SliceRef, cache *clientcache.ObjectCache, manifest *lazyfs.Manifest) *mountFetcher {
	return &mountFetcher{
		r:         r,
		conn:      conn,
		slice:     slice,
		cache:     cache,
		commitID:  manifest.CommitID(),
		manifest:  manifest,
		inflight:  map[string]*fetchCall{},
		completed: map[string]bool{},
	}
}

// setBase points the fetcher at a new base, after gs sync moved the layers.
func (f *mountFetcher) setBase(manifest *lazyfs.Manifest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifest, f.commitID, f.byHash, f.completed = manifest, manifest.CommitID(), nil, map[string]bool{}
	f.ahead.reset()
}

// Fetch implements fusefs.Fetcher.
func (f *mountFetcher) Fetch(ctx context.Context, p string, attr lazyfs.Attr) (string, bool, error) {
	f.observeRead(p)
	fetched, err := f.fetchContent(ctx, p, attr.ContentHash, attr.Size)
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

// fetchContent puts the content with hash in the cache, unless it is there.
// p is a path that shows it, if known. It reports whether it fetched.
func (f *mountFetcher) fetchContent(ctx context.Context, p, hash string, size int64) (bool, error) {
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
		return false, fmt.Errorf("the server is not reachable (trying again in %s): %w", wait.Round(time.Second), errServerUnreachable)
	}
	commitID := f.commitID
	primary, siblings := f.candidatesLocked(p, hash, size)
	f.mu.Unlock()

	// Check cache existence on disk outside f.mu so os.Stat syscalls do not
	// serialize concurrent FUSE opens.
	if f.cache.Exists(hash) {
		return false, nil
	}
	filtered := make([]lazyfs.BaseEntry, 0, min(len(siblings)+1, mountBatchMaxFiles))
	filtered = append(filtered, primary)
	total := primary.Size
	for _, sibling := range siblings {
		if len(filtered) >= mountBatchMaxFiles || total+sibling.Size > mountBatchMaxBytes {
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
		return false, fmt.Errorf("the server is not reachable (trying again in %s): %w", wait.Round(time.Second), errServerUnreachable)
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

// runBatch fetches batch, which the caller has marked in flight under call,
// and settles call. One reader giving up must not fail the others waiting on
// the fetch, so it has its own deadline.
func (f *mountFetcher) runBatch(commitID string, batch []lazyfs.BaseEntry, call *fetchCall) error {
	fetchCtx, cancel := context.WithTimeout(context.Background(), mountFetchTimeout)
	defer cancel()
	// A dropped connection is retried, over a few seconds: what a retry fetches
	// again is only what the cache lacks.
	var files int
	var bytes int64
	err := retryUnavailable(fetchCtx, fetchRetryBackoff, func() error {
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
		f.downUntil = time.Now().Add(mountOutageWindow)
	}
	f.mu.Unlock()
	call.err = err
	close(call.done)
	return err
}

// fetchRetryBackoff paces the retries of a fetch whose connection dropped, and
// mountOutageWindow is how long fetches then fail at once instead of trying.
var fetchRetryBackoff = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}

const mountOutageWindow = 5 * time.Second

var errServerUnreachable = errors.New("server unreachable")

// candidatesLocked picks the primary entry and un-inflight sibling candidates
// from in-memory manifest metadata only (without calling cache.Exists under f.mu).
func (f *mountFetcher) candidatesLocked(p, hash string, size int64) (lazyfs.BaseEntry, []lazyfs.BaseEntry) {
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
	if entry.Size > mountLargeFileBytes {
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
		if !ok || sibling.Kind != lazyfs.KindFile || sibling.Size > mountPrefetchMaxFile || seen[sibling.ContentHash] {
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

// fetch puts the entries' content in the cache, checking every hash. The
// credential is read for every fetch, so gs auth use-key takes effect without
// a remount.
func (f *mountFetcher) fetch(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error) {
	if f.fetchFn != nil {
		return f.fetchFn(ctx, commitID, entries)
	}
	cfg, err := f.r.readUserConfig()
	if err != nil {
		return 0, 0, err
	}
	return fetchBaseFiles(authContext(ctx, cfg), f.conn, f.slice, commitID, entries, f.cache)
}

// mountContent is the daemon's lazyfs.Content: cacheContent that fetches
// what the cache lacks, so the first write to a file nothing has read yet
// can copy its content up.
type mountContent struct {
	cacheContent
	fetcher *mountFetcher
}

func (c mountContent) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	if _, err := c.fetcher.fetchContent(ctx, "", hash, 0); err != nil {
		return nil, err
	}
	return c.cacheContent.Open(ctx, hash)
}

// fetchBaseFiles fetches base files into the cache through a TransferManager:
// small ones in ReadFiles batches, large ones, and content known by hash only
// (no path), as streams. It returns how many it fetched and their size.
func fetchBaseFiles(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
	targets := make([]remoteReadTarget, 0, len(entries))
	for _, entry := range entries {
		targets = append(targets, remoteReadTarget{Path: entry.Path, ContentHash: entry.ContentHash, Size: entry.Size})
	}
	tm := NewTransferManager(corev1.NewRepositoryServiceClient(conn), corev1.NewBlobServiceClient(conn), slice, cache)
	res, err := tm.FetchFilesToCache(ctx, TransferFetchRequest{CommitID: commitID, Targets: targets, Workers: 1})
	return res.FetchedFiles, res.FetchedBytes, err
}

// fetchConcurrently splits entries into groups fetched side by side.
func fetchConcurrently(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
	groups := make([][]lazyfs.BaseEntry, mountFetchConcurrency)
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
			n, size, err := fetchBaseFiles(ctx, conn, slice, commitID, group, cache)
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
