package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding/gzip"
	grpcstatus "google.golang.org/grpc/status"
)

type hydrateResult struct {
	FileCount   int `json:"file_count"`
	CacheHits   int `json:"cache_hits"`
	CacheMisses int `json:"cache_misses"`
}

func (r Runner) hydrateWorkspacePaths(ctx context.Context, conn *grpc.ClientConn, ws WorkspaceConfig, commitID string, requested []string, cache *clientcache.ObjectCache) (hydrateResult, error) {
	return r.hydrateWith(ctx, conn, ws, commitID, cache, func(h *workspaceHydrator) error {
		for _, requestedPath := range requested {
			globalPath, err := workspaceInputToGlobalPath(ws, requestedPath)
			if err != nil {
				return err
			}
			if err := h.hydratePath(ctx, globalPath); err != nil {
				return err
			}
		}
		return nil
	})
}

// receivedCheckout is what a Checkout stream gave: the slice, the commit and
// the listing; any file content it sent is in the client cache.
type receivedCheckout struct {
	slice    *corev1.Slice
	commitID string
	entries  []*corev1.TreeEntry
	// fetched are the contents the stream brought, now in the cache.
	fetched map[string]bool
}

// receiveCheckout reads a Checkout stream (design/28_benchmarks.md). It asks
// for the files too only when the client cache is empty (a fresh machine or
// sandbox): a warm cache fetches just what it lacks, afterwards. Content goes
// into the cache, checked against its hash.
func receiveCheckout(ctx context.Context, conn *grpc.ClientConn, ref *corev1.SliceRef, cache *clientcache.ObjectCache) (*receivedCheckout, error) {
	stream, err := corev1.NewRepositoryServiceClient(conn).Checkout(ctx, &corev1.CheckoutRequest{Slice: ref, IncludeContent: cache.Empty()}, grpc.UseCompressor(gzip.Name))
	if err != nil {
		return nil, err
	}
	out := &receivedCheckout{fetched: map[string]bool{}}
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch part := msg.GetPart().(type) {
		case *corev1.CheckoutResponse_Header:
			out.slice, out.commitID = part.Header.GetSlice(), part.Header.GetCommitId()
		case *corev1.CheckoutResponse_Entries:
			out.entries = append(out.entries, part.Entries.GetEntries()...)
		case *corev1.CheckoutResponse_Contents:
			for _, content := range part.Contents.GetContents() {
				cached, err := cache.PutBytes(content.GetData())
				if err != nil {
					return nil, err
				}
				if cached.ContentHash != content.GetContentHash() {
					return nil, fmt.Errorf("checkout content hash mismatch: got %s, want %s", cached.ContentHash, content.GetContentHash())
				}
				out.fetched[cached.ContentHash] = true
			}
		}
	}
	if out.slice == nil || out.slice.GetDefinition() == nil || out.commitID == "" {
		return nil, fmt.Errorf("checkout stream ended without its header")
	}
	return out, nil
}

// hydrateListing writes the files of a listing Checkout streamed: everything
// under the workspace's included paths.
func (r Runner) hydrateListing(ctx context.Context, conn *grpc.ClientConn, ws WorkspaceConfig, checkout *receivedCheckout, cache *clientcache.ObjectCache) (hydrateResult, error) {
	entries := checkout.entries
	return r.hydrateWith(ctx, conn, ws, checkout.commitID, cache, func(h *workspaceHydrator) error {
		h.fetched = checkout.fetched
		for _, included := range ws.IncludedPaths {
			prefix := strings.TrimSuffix(included, "/") + "/"
			for _, entry := range entries {
				if strings.HasPrefix(entry.GetPath(), prefix) {
					if err := h.makeDirectory(included); err != nil {
						return err
					}
					break
				}
			}
		}
		return h.addListed(entries)
	})
}

// hydrateWith writes the files add queues (and those it lists below them),
// then records them in the base snapshot and the stat cache.
func (r Runner) hydrateWith(ctx context.Context, conn *grpc.ClientConn, ws WorkspaceConfig, commitID string, cache *clientcache.ObjectCache, add func(*workspaceHydrator) error) (hydrateResult, error) {
	base, err := r.readBaseSnapshot()
	if err != nil {
		return hydrateResult{}, err
	}
	if base.CommitID == "" {
		base.CommitID = commitID
	}
	if base.Files == nil {
		base.Files = map[string]BaseSnapshotFile{}
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	root, err := r.workspaceRoot()
	if err != nil {
		return hydrateResult{}, err
	}
	hydrator := workspaceHydrator{
		runner:   r,
		root:     root,
		repo:     repo,
		blob:     corev1.NewBlobServiceClient(conn),
		cache:    cache,
		ws:       ws,
		commitID: commitID,
		base:     base,
		progress: startProgress(ctx, "hydrating", "files", 0),
	}
	defer hydrator.progress.Finish()
	if err := add(&hydrator); err != nil {
		return hydrateResult{}, err
	}
	if err := hydrator.materialize(ctx); err != nil {
		return hydrateResult{}, err
	}
	// The files just written need not be hashed by the next scan.
	if len(hydrator.stats) > 0 {
		cache := readStatCache(root)
		for rel, entry := range hydrator.stats {
			cache.Files[rel] = entry
		}
		writeStatCache(root, cache.Files)
	}
	if err := r.writeBaseSnapshot(hydrator.base); err != nil {
		return hydrateResult{}, err
	}
	return hydrator.result, nil
}

type workspaceHydrator struct {
	runner   Runner
	root     string
	repo     corev1.RepositoryServiceClient
	blob     corev1.BlobServiceClient
	cache    *clientcache.ObjectCache
	ws       WorkspaceConfig
	commitID string
	base     BaseSnapshot
	result   hydrateResult
	progress *progressReporter
	// pending are the files hydratePath found; materialize writes them.
	pending []*corev1.TreeEntry
	// stats are the written files' stat cache entries, by relative path.
	stats map[string]statCacheEntry
	// fetched are contents just downloaded into the cache (by Checkout);
	// they count as misses, not hits (true until counted).
	fetched map[string]bool
	// mu guards base and result while materialize's batches write files.
	mu sync.Mutex
}

func (h *workspaceHydrator) hydratePath(ctx context.Context, globalPath string) error {
	// One recursive listing says what is at the path (a file lists as
	// itself) and everything under it (design/28_benchmarks.md).
	entries, recursive, err := listDirectoryPages(ctx, h.repo, &corev1.ListDirectoryRequest{CommitId: h.commitID, Path: globalPath, PageSize: 10000, Recursive: true})
	if err != nil {
		return err
	}
	if !recursive {
		return fmt.Errorf("the server listed %s one level only; upgrade the server", globalPath)
	}
	if len(entries) == 1 && entries[0].Kind == corev1.EntryKind_ENTRY_KIND_FILE && entries[0].Path == globalPath {
		h.queueFile(entries[0])
		return nil
	}
	if len(entries) == 0 {
		// An empty directory, or nothing there.
		resolved, err := h.repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: h.commitID, Path: globalPath})
		if grpcstatus.Code(err) == codes.NotFound || (err == nil && resolved.GetEntry().GetKind() != corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if err := h.makeDirectory(globalPath); err != nil {
		return err
	}
	return h.addListed(entries)
}

// addListed queues a recursive listing's files and creates its directories.
func (h *workspaceHydrator) addListed(entries []*corev1.TreeEntry) error {
	for _, entry := range entries {
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_FILE:
			h.queueFile(entry)
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			if err := h.makeDirectory(entry.GetPath()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *workspaceHydrator) makeDirectory(globalPath string) error {
	rel, err := workspaceRelPath(h.ws, globalPath)
	if err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(h.root, filepath.FromSlash(rel)), 0o755)
}

// queueFile adds a file for materialize to write.
func (h *workspaceHydrator) queueFile(entry *corev1.TreeEntry) {
	h.pending = append(h.pending, entry)
}

const (
	hydrateBatchFiles   = 256
	hydrateBatchBytes   = 4 << 20
	hydrateBatchWorkers = 4
)

// materialize writes the files hydratePath found. Files the client cache has
// are copied from it and large files are streamed one by one; the rest are
// fetched in batches (ReadFiles), a few batches at a time, so a checkout does
// not pay a round trip per file (design/28_benchmarks.md).
func (h *workspaceHydrator) materialize(ctx context.Context) error {
	misses := map[string][]*corev1.TreeEntry{}
	var order []string
	for _, entry := range h.pending {
		if entry.ContentHash == "" {
			return fmt.Errorf("file %s has no content hash", entry.Path)
		}
		if entry.Size > blobStreamThresholdBytes {
			if err := h.writeLargeFile(ctx, entry); err != nil {
				return err
			}
			continue
		}
		if h.cache.Exists(entry.ContentHash) {
			data, err := h.cache.Read(entry.ContentHash)
			if err != nil {
				return err
			}
			// Content Checkout just brought is a miss, once per content, as
			// a batch counts it.
			if first, fetched := h.fetched[entry.ContentHash]; !fetched {
				h.result.CacheHits++
			} else if first {
				h.result.CacheMisses++
				h.fetched[entry.ContentHash] = false
			}
			if err := h.writeFile(entry, data); err != nil {
				return err
			}
			continue
		}
		if _, seen := misses[entry.ContentHash]; !seen {
			order = append(order, entry.ContentHash)
		}
		misses[entry.ContentHash] = append(misses[entry.ContentHash], entry)
	}
	h.pending = nil

	var batches [][]string
	var batch []string
	var batchBytes int64
	for _, hash := range order {
		size := misses[hash][0].Size
		if len(batch) > 0 && (len(batch) >= hydrateBatchFiles || batchBytes+size > hydrateBatchBytes) {
			batches = append(batches, batch)
			batch, batchBytes = nil, 0
		}
		batch = append(batch, hash)
		batchBytes += size
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(hydrateBatchWorkers)
	for _, hashes := range batches {
		group.Go(func() error { return h.fetchBatch(groupCtx, hashes, misses) })
	}
	return group.Wait()
}

// fetchBatch reads one file per content hash, caches it, and writes it to
// every path that has it.
func (h *workspaceHydrator) fetchBatch(ctx context.Context, hashes []string, misses map[string][]*corev1.TreeEntry) error {
	paths := make([]string, len(hashes))
	for i, hash := range hashes {
		paths[i] = misses[hash][0].Path
	}
	// Compressed: source shrinks several times.
	res, err := h.repo.ReadFiles(ctx, &corev1.ReadFilesRequest{CommitId: h.commitID, Paths: paths}, grpc.UseCompressor(gzip.Name))
	if err != nil {
		return err
	}
	if len(res.GetFiles()) != len(paths) {
		return fmt.Errorf("server returned %d of %d files", len(res.GetFiles()), len(paths))
	}
	for i, hash := range hashes {
		data := res.GetFiles()[i].GetData()
		cached, err := h.cache.PutBytes(data)
		if err != nil {
			return err
		}
		if cached.ContentHash != hash {
			return fmt.Errorf("hydrated content hash mismatch for %s: got %s, want %s", paths[i], cached.ContentHash, hash)
		}
		h.mu.Lock()
		h.result.CacheMisses++
		h.mu.Unlock()
		for _, entry := range misses[hash] {
			if err := h.writeFile(entry, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *workspaceHydrator) fileTarget(entry *corev1.TreeEntry) (string, string, uint32, error) {
	rel, err := workspaceRelPath(h.ws, entry.Path)
	if err != nil {
		return "", "", 0, err
	}
	target := filepath.Join(h.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", "", 0, err
	}
	mode := uint32(entry.Mode)
	if mode == 0 {
		mode = 0o100644
	}
	return rel, target, mode, nil
}

func (h *workspaceHydrator) writeFile(entry *corev1.TreeEntry, data []byte) error {
	rel, target, mode, err := h.fileTarget(entry)
	if err != nil {
		return err
	}
	if _, err := writeWorkspaceFile(target, mode, bytes.NewReader(data)); err != nil {
		return err
	}
	h.recordFile(entry, rel, target, mode, int64(len(data)))
	return nil
}

func (h *workspaceHydrator) writeLargeFile(ctx context.Context, entry *corev1.TreeEntry) error {
	rel, target, mode, err := h.fileTarget(entry)
	if err != nil {
		return err
	}
	size, err := h.hydrateLargeFile(ctx, entry, target, mode)
	if err != nil {
		return err
	}
	h.recordFile(entry, rel, target, mode, size)
	return nil
}

func (h *workspaceHydrator) recordFile(entry *corev1.TreeEntry, rel, target string, mode uint32, size int64) {
	info, statErr := os.Lstat(target)
	h.mu.Lock()
	if statErr == nil {
		if stat, ok := statCacheEntryFor(info, entry.ContentHash); ok {
			if h.stats == nil {
				h.stats = map[string]statCacheEntry{}
			}
			h.stats[rel] = stat
		}
	}
	h.base.Files[entry.Path] = BaseSnapshotFile{
		Path:        entry.Path,
		RelPath:     rel,
		ContentHash: entry.ContentHash,
		Mode:        mode,
		Size:        size,
	}
	h.result.FileCount++
	h.mu.Unlock()
	h.progress.Add(1)
}

func (h *workspaceHydrator) hydrateLargeFile(ctx context.Context, entry *corev1.TreeEntry, target string, mode uint32) (int64, error) {
	if h.cache.Exists(entry.ContentHash) {
		h.result.CacheHits++
		return h.copyCachedFileToWorkspace(entry.ContentHash, target, mode)
	}
	stream, err := h.blob.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{
		Slice:       &corev1.SliceRef{Account: h.ws.Account, Slice: h.ws.Slice},
		ContentHash: entry.ContentHash,
	})
	if err != nil {
		return 0, err
	}
	cached, err := h.cache.PutReader(newBlobStreamReader(stream))
	if err != nil {
		return 0, err
	}
	if cached.ContentHash != entry.ContentHash {
		return 0, fmt.Errorf("hydrated content hash mismatch for %s: got %s, want %s", entry.Path, cached.ContentHash, entry.ContentHash)
	}
	h.result.CacheMisses++
	return h.copyCachedFileToWorkspace(entry.ContentHash, target, mode)
}

func (h *workspaceHydrator) copyCachedFileToWorkspace(contentHash, target string, mode uint32) (int64, error) {
	f, err := h.cache.Open(contentHash)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return writeWorkspaceFile(target, mode, f)
}

// isSymlinkMode reports whether a native file mode is a symbolic link.
func isSymlinkMode(mode uint32) bool {
	return mode&0o170000 == 0o120000
}

// writeWorkspaceFile materializes one file. A symlink whose target is a
// relative path becomes a real symlink (its content is the link target, as in
// Git). Other symlinks are written as regular files containing the target; the
// workspace scan treats that fallback as unchanged. A regular file never writes
// through an existing symlink at the same path.
func writeWorkspaceFile(target string, mode uint32, r io.Reader) (int64, error) {
	if info, err := os.Lstat(target); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		if err := os.Remove(target); err != nil {
			return 0, err
		}
	}
	if isSymlinkMode(mode) {
		data, err := io.ReadAll(r)
		if err != nil {
			return 0, err
		}
		link := string(data)
		if link != "" && !filepath.IsAbs(link) && !strings.ContainsRune(link, 0) {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
			if err := os.Symlink(link, target); err == nil {
				return int64(len(data)), nil
			}
		}
		r = bytes.NewReader(data)
		mode = 0o100644
	}
	fileMode := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		fileMode = 0o755
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return n, copyErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	if err := os.Chmod(target, fileMode); err != nil {
		return n, err
	}
	return n, nil
}
