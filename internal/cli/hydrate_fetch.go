package cli

import (
	"context"
	"errors"
	"fmt"
	"gitslice.io/gitslice/internal/workspace"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
)

// receivedCheckout is what a Checkout stream gave: the slice, the commit and
// the listing; any file content it sent is in the client cache.
type receivedCheckout struct {
	slice    *corev1.Slice
	commitID string
	entries  []*corev1.TreeEntry
	// fetched are the contents the stream brought, now in the cache.
	fetched map[string]bool
	// fromListingCache: the entries came from this machine's listing cache.
	fromListingCache bool
	// deltaEntries and deltaRemoved count what the server sent when it sent
	// only the changes since a cached listing; both 0 for a whole listing.
	deltaEntries, deltaRemoved int
}

// receiveCheckout reads a Checkout stream (design/28_benchmarks.md). It asks
// for the files too only when the client cache is empty (a fresh machine or
// sandbox): a warm cache fetches just what it lacks, afterwards. Content goes
// into the cache, checked against its hash.
func receiveCheckout(ctx context.Context, conn *grpc.ClientConn, ref *corev1.SliceRef, cache *clientcache.ObjectCache) (*receivedCheckout, error) {
	return receiveCheckoutStream(ctx, conn, ref, "", cache, cache.Empty())
}

// receiveCheckoutListing reads the listing only, never the content: what a
// lazy workspace needs (design/30_lazy_workspaces.md).
func receiveCheckoutListing(ctx context.Context, conn *grpc.ClientConn, ref *corev1.SliceRef, cache *clientcache.ObjectCache) (*receivedCheckout, error) {
	return receiveCheckoutStream(ctx, conn, ref, "", cache, false)
}

// receiveCheckoutListingAt is receiveCheckoutListing at a given commit.
func receiveCheckoutListingAt(ctx context.Context, conn *grpc.ClientConn, ref *corev1.SliceRef, commitID string, cache *clientcache.ObjectCache) (*receivedCheckout, error) {
	return receiveCheckoutStream(ctx, conn, ref, commitID, cache, false)
}

func receiveCheckoutStream(ctx context.Context, conn *grpc.ClientConn, ref *corev1.SliceRef, commitID string, cache *clientcache.ObjectCache, includeContent bool) (*receivedCheckout, error) {
	// A listing this machine already has (same slice definition, same commit)
	// is read from the local listing cache once the header arrives, and the
	// rest of the stream is not waited for.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req := &corev1.CheckoutRequest{Slice: ref, CommitId: commitID, IncludeContent: includeContent}
	// A listing the cache has for this slice, at any commit, lets the server
	// send only what changed since (CheckoutHeader.delta).
	var since *workspace.ListingBase
	if !includeContent {
		if base, ok := workspace.NewestCachedListing(cache, ref); ok {
			since = base
			req.SinceCommitId, req.SinceDefinitionHash = base.CommitID, base.DefinitionHash
		}
	}
	stream, err := corev1.NewRepositoryServiceClient(conn).Checkout(ctx, req, grpc.UseCompressor(gzip.Name))
	if err != nil {
		return nil, err
	}
	out := &receivedCheckout{fetched: map[string]bool{}}
	delta := false
	var removed []string
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
			delta = part.Header.GetDelta() && since != nil
			if part.Header.GetDelta() && since == nil {
				return nil, fmt.Errorf("checkout sent changes since a listing this client did not name")
			}
			workspace.RememberListingRef(cache, ref, out.slice)
			if !includeContent {
				if entries, ok := workspace.ReadCachedListing(cache, out.slice, out.commitID); ok {
					out.entries, out.fromListingCache = entries, true
					cancel()
					if out.slice.GetDefinition() == nil || out.commitID == "" {
						return nil, fmt.Errorf("checkout stream ended without its header")
					}
					return out, nil
				}
			}
		case *corev1.CheckoutResponse_Entries:
			out.entries = append(out.entries, part.Entries.GetEntries()...)
		case *corev1.CheckoutResponse_Removed:
			if !delta {
				return nil, fmt.Errorf("checkout sent removed paths outside a delta")
			}
			removed = append(removed, part.Removed.GetPaths()...)
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
	if delta {
		out.deltaEntries, out.deltaRemoved = len(out.entries), len(removed)
		out.entries = workspace.ApplyListingDelta(since.Entries, removed, out.entries)
	}
	if !includeContent {
		workspace.WriteCachedListing(cache, out.slice, out.commitID, out.entries)
	}
	return out, nil
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
		if data, ok, err := readVerifiedCachedBytes(h.cache, entry.ContentHash); err == nil && ok {
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

// fetchBatch reads one file per content hash through a TransferManager (which
// caches and verifies it), and writes it to every path that has it.
func (h *workspaceHydrator) fetchBatch(ctx context.Context, hashes []string, misses map[string][]*corev1.TreeEntry) error {
	targets := make([]remoteReadTarget, len(hashes))
	for i, hash := range hashes {
		first := misses[hash][0]
		targets[i] = remoteReadTarget{Path: first.Path, ContentHash: hash, Size: first.Size}
	}
	tm := NewTransferManager(h.repo, nil, nil, h.cache)
	if _, err := tm.FetchFilesToCache(ctx, TransferFetchRequest{CommitID: h.commitID, Targets: targets, SkipExistingCheck: true, Workers: 1}); err != nil {
		return err
	}
	for _, hash := range hashes {
		data, err := h.cache.Read(hash)
		if err != nil {
			return err
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

func (h *workspaceHydrator) hydrateLargeFile(ctx context.Context, entry *corev1.TreeEntry, rel string, mode uint32) (int64, error) {
	if h.cache.Exists(entry.ContentHash) {
		h.result.CacheHits++
		return h.copyCachedFileToWorkspace(entry.ContentHash, rel, mode)
	}
	stream, err := h.blob.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{
		Slice:       &corev1.SliceRef{Account: h.ws.Account, Slice: h.ws.Slice},
		ContentHash: entry.ContentHash,
	})
	if err != nil {
		return 0, err
	}
	if workspace.IsSymlinkMode(mode) {
		cached, err := h.cache.PutReader(newBlobStreamReader(stream))
		if err != nil {
			return 0, err
		}
		if cached.ContentHash != entry.ContentHash {
			return 0, fmt.Errorf("hydrated content hash mismatch for %s: got %s, want %s", entry.Path, cached.ContentHash, entry.ContentHash)
		}
		h.result.CacheMisses++
		return h.copyCachedFileToWorkspace(entry.ContentHash, rel, mode)
	}

	absRoot, err := filepath.Abs(h.root)
	if err != nil {
		return 0, err
	}
	osRel, err := cleanConfinedRelPath(rel)
	if err != nil {
		return 0, err
	}
	osRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return 0, err
	}
	defer osRoot.Close()

	parentRel := filepath.Dir(osRel)
	if parentRel != "." {
		if err := workspace.MkdirAllNoSymlinksInRoot(osRoot, parentRel); err != nil {
			return 0, err
		}
	}
	if info, lerr := osRoot.Lstat(osRel); lerr == nil {
		if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
			return 0, fmt.Errorf("cannot overwrite directory %q with file", rel)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if err := osRoot.Remove(osRel); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
		}
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return 0, lerr
	}

	fileMode := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		fileMode = 0o755
	}
	wsFile, err := osRoot.OpenFile(osRel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode)
	if err != nil {
		return 0, err
	}
	cached, putErr := h.cache.PutReader(io.TeeReader(newBlobStreamReader(stream), wsFile))
	chmodErr := wsFile.Chmod(fileMode)
	closeErr := wsFile.Close()
	if putErr != nil {
		_ = osRoot.Remove(osRel)
		return 0, putErr
	}
	if chmodErr != nil {
		_ = osRoot.Remove(osRel)
		return 0, chmodErr
	}
	if closeErr != nil {
		_ = osRoot.Remove(osRel)
		return 0, closeErr
	}
	if cached.ContentHash != entry.ContentHash {
		_ = osRoot.Remove(osRel)
		return 0, fmt.Errorf("hydrated content hash mismatch for %s: got %s, want %s", entry.Path, cached.ContentHash, entry.ContentHash)
	}
	h.result.CacheMisses++
	return cached.Size, nil
}

func (h *workspaceHydrator) copyCachedFileToWorkspace(contentHash, rel string, mode uint32) (int64, error) {
	f, err := h.cache.Open(contentHash)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	_, n, err := writeConfinedWorkspaceFile(h.root, rel, mode, f)
	return n, err
}
