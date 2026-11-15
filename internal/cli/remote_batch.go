package cli

import (
	"context"
	"fmt"
	"sync"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding/gzip"
	grpcstatus "google.golang.org/grpc/status"
)

type remoteReadTarget struct {
	Path        string
	ContentHash string
	Size        int64
}

func readVerifiedCachedBytes(cache *clientcache.ObjectCache, expectedHash string) ([]byte, bool, error) {
	if cache == nil || expectedHash == "" || !cache.Exists(expectedHash) {
		return nil, false, nil
	}
	data, err := cache.Read(expectedHash)
	if err != nil {
		return nil, false, err
	}
	if got := objectid.RawContentHash(data); got != expectedHash {
		return nil, false, fmt.Errorf("cached content hash mismatch: got %s, want %s", got, expectedHash)
	}
	return data, true, nil
}

func putVerifiedCachedBytes(cache *clientcache.ObjectCache, path, expectedHash string, data []byte) ([]byte, error) {
	if cache == nil {
		if expectedHash != "" {
			if got := objectid.RawContentHash(data); got != expectedHash {
				return nil, fmt.Errorf("content hash mismatch for %s: got %s, want %s", path, got, expectedHash)
			}
		}
		return data, nil
	}
	cached, err := cache.PutBytes(data)
	if err != nil {
		return nil, err
	}
	if expectedHash != "" && cached.ContentHash != expectedHash {
		return nil, fmt.Errorf("content hash mismatch for %s: got %s, want %s", path, cached.ContentHash, expectedHash)
	}
	return data, nil
}

// prefetchRemoteFilesToCache fetches uncached files at commitID into cache using
// bounded concurrent ReadFiles batches (4 MiB / 256 files, gzip compressed) and
// streams large files (> 4 MiB) via ReadBlobStream. It returns any fetched file
// bytes keyed by path for targets whose ContentHash was unknown in advance.
func prefetchRemoteFilesToCache(
	ctx context.Context,
	repo corev1.RepositoryServiceClient,
	blob corev1.BlobServiceClient,
	sliceRef *corev1.SliceRef,
	cache *clientcache.ObjectCache,
	commitID string,
	targets []remoteReadTarget,
) (map[string][]byte, error) {
	unhashedOut := map[string][]byte{}
	if commitID == "" || len(targets) == 0 || repo == nil {
		return unhashedOut, nil
	}

	var large []remoteReadTarget
	var small []remoteReadTarget
	seenHashes := map[string]struct{}{}
	seenPaths := map[string]struct{}{}

	for _, t := range targets {
		if t.Path == "" {
			continue
		}
		if t.ContentHash != "" {
			if _, ok := seenHashes[t.ContentHash]; ok {
				continue
			}
			if cache != nil && cache.Exists(t.ContentHash) {
				if _, ok, err := readVerifiedCachedBytes(cache, t.ContentHash); err == nil && ok {
					seenHashes[t.ContentHash] = struct{}{}
					continue
				}
			}
			seenHashes[t.ContentHash] = struct{}{}
		} else {
			if _, ok := seenPaths[t.Path]; ok {
				continue
			}
			seenPaths[t.Path] = struct{}{}
		}

		if t.Size > blobStreamThresholdBytes && blob != nil && t.ContentHash != "" {
			large = append(large, t)
			continue
		}
		small = append(small, t)
	}

	for _, t := range large {
		if err := streamRemoteBlobToCache(ctx, repo, blob, sliceRef, cache, commitID, t); err != nil {
			return nil, err
		}
	}

	if len(small) == 0 {
		return unhashedOut, nil
	}

	var batches [][]remoteReadTarget
	var batch []remoteReadTarget
	var batchBytes int64
	for _, t := range small {
		estSize := t.Size
		if estSize <= 0 {
			estSize = 16 << 10
		}
		if len(batch) > 0 && (len(batch) >= hydrateBatchFiles || batchBytes+estSize > hydrateBatchBytes) {
			batches = append(batches, batch)
			batch, batchBytes = nil, 0
		}
		batch = append(batch, t)
		batchBytes += estSize
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}

	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(hydrateBatchWorkers)
	for _, b := range batches {
		batchTargets := b
		group.Go(func() error {
			return fetchRemoteReadBatch(groupCtx, repo, blob, sliceRef, cache, commitID, batchTargets, &mu, unhashedOut)
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return unhashedOut, nil
}

func fetchRemoteReadBatch(
	ctx context.Context,
	repo corev1.RepositoryServiceClient,
	blob corev1.BlobServiceClient,
	sliceRef *corev1.SliceRef,
	cache *clientcache.ObjectCache,
	commitID string,
	targets []remoteReadTarget,
	mu *sync.Mutex,
	unhashedOut map[string][]byte,
) error {
	paths := make([]string, len(targets))
	for i, t := range targets {
		paths[i] = t.Path
	}
	res, err := repo.ReadFiles(ctx, &corev1.ReadFilesRequest{
		CommitId: commitID,
		Paths:    paths,
		Slice:    sliceRef,
	}, grpc.UseCompressor(gzip.Name))
	if err != nil {
		code := grpcstatus.Code(err)
		if code == codes.Unimplemented || code == codes.ResourceExhausted || code == codes.NotFound {
			for _, t := range targets {
				data, ok, singleErr := fetchSingleRemoteTarget(ctx, repo, blob, sliceRef, cache, commitID, t)
				if singleErr != nil {
					return singleErr
				}
				if ok && t.ContentHash == "" {
					mu.Lock()
					unhashedOut[t.Path] = data
					mu.Unlock()
				}
			}
			return nil
		}
		return err
	}
	files := res.GetFiles()
	if len(files) != len(targets) {
		byPath := make(map[string][]byte, len(files))
		for _, f := range files {
			byPath[f.GetPath()] = f.GetData()
		}
		for _, t := range targets {
			data, ok := byPath[t.Path]
			if !ok {
				return fmt.Errorf("server omitted requested file %s from ReadFiles response", t.Path)
			}
			if _, err := putVerifiedCachedBytes(cache, t.Path, t.ContentHash, data); err != nil {
				return err
			}
			if t.ContentHash == "" {
				mu.Lock()
				unhashedOut[t.Path] = data
				mu.Unlock()
			}
		}
		return nil
	}
	for i, t := range targets {
		data := files[i].GetData()
		if _, err := putVerifiedCachedBytes(cache, t.Path, t.ContentHash, data); err != nil {
			return err
		}
		if t.ContentHash == "" {
			mu.Lock()
			unhashedOut[t.Path] = data
			mu.Unlock()
		}
	}
	return nil
}

func streamRemoteBlobToCache(
	ctx context.Context,
	repo corev1.RepositoryServiceClient,
	blob corev1.BlobServiceClient,
	sliceRef *corev1.SliceRef,
	cache *clientcache.ObjectCache,
	commitID string,
	target remoteReadTarget,
) error {
	if blob != nil && target.ContentHash != "" && cache != nil {
		stream, err := blob.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{
			Slice:       sliceRef,
			ContentHash: target.ContentHash,
		})
		if err == nil {
			cached, putErr := cache.PutReader(newBlobStreamReader(stream))
			if putErr == nil {
				if cached.ContentHash != target.ContentHash {
					return fmt.Errorf("content hash mismatch for %s: got %s, want %s", target.Path, cached.ContentHash, target.ContentHash)
				}
				return nil
			}
			if grpcstatus.Code(putErr) != codes.NotFound && grpcstatus.Code(putErr) != codes.Unimplemented {
				return putErr
			}
		} else if grpcstatus.Code(err) != codes.NotFound && grpcstatus.Code(err) != codes.Unimplemented {
			return err
		}
	}
	_, _, err := fetchSingleRemoteTarget(ctx, repo, blob, sliceRef, cache, commitID, target)
	return err
}

func fetchSingleRemoteTarget(
	ctx context.Context,
	repo corev1.RepositoryServiceClient,
	blob corev1.BlobServiceClient,
	sliceRef *corev1.SliceRef,
	cache *clientcache.ObjectCache,
	commitID string,
	target remoteReadTarget,
) ([]byte, bool, error) {
	if blob != nil && target.ContentHash != "" {
		data, err := readBlobStreamBytes(ctx, blob, sliceRef, target.ContentHash, 0, 0)
		if err == nil {
			if _, verifyErr := putVerifiedCachedBytes(cache, target.Path, target.ContentHash, data); verifyErr != nil {
				return nil, false, verifyErr
			}
			return data, true, nil
		}
		if grpcstatus.Code(err) != codes.NotFound && grpcstatus.Code(err) != codes.Unimplemented {
			return nil, false, err
		}
	}
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: commitID,
		Path:     target.Path,
		Slice:    sliceRef,
	}, grpc.UseCompressor(gzip.Name))
	if grpcstatus.Code(err) == codes.ResourceExhausted && blob != nil {
		resolved, resErr := repo.ResolvePath(ctx, &corev1.ResolvePathRequest{
			CommitId: commitID,
			Path:     target.Path,
			Slice:    sliceRef,
		})
		if grpcstatus.Code(resErr) == codes.NotFound {
			return nil, false, nil
		}
		if resErr != nil {
			return nil, false, resErr
		}
		hash := resolved.GetEntry().GetContentHash()
		if hash == "" {
			return nil, false, err
		}
		expected := target.ContentHash
		if expected == "" {
			expected = hash
		}
		data, streamErr := readBlobStreamBytes(ctx, blob, sliceRef, hash, 0, 0)
		if streamErr != nil {
			return nil, false, streamErr
		}
		if _, verifyErr := putVerifiedCachedBytes(cache, target.Path, expected, data); verifyErr != nil {
			return nil, false, verifyErr
		}
		return data, true, nil
	}
	if grpcstatus.Code(err) == codes.NotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	data := read.GetData()
	if _, verifyErr := putVerifiedCachedBytes(cache, target.Path, target.ContentHash, data); verifyErr != nil {
		return nil, false, verifyErr
	}
	return data, true, nil
}

// batchReadCommitDiffFiles fetches changed paths at commitID for gs diff <commit>.
// Pure-modification batches complete in a single compressed ReadFiles RPC; batches
// containing added/deleted paths or >32 MiB files bisect or fall back to
// ResolvePath + ReadBlobStream / ReadFile.
func batchReadCommitDiffFiles(
	ctx context.Context,
	repo corev1.RepositoryServiceClient,
	blob corev1.BlobServiceClient,
	cache *clientcache.ObjectCache,
	commit *corev1.Commit,
	paths []string,
) (map[string]diffutil.File, error) {
	out := make(map[string]diffutil.File, len(paths))
	if commit == nil || commit.Id == "" || len(paths) == 0 {
		for _, p := range paths {
			out[p] = diffutil.File{Path: p}
		}
		return out, nil
	}

	var batches [][]string
	for start := 0; start < len(paths); start += hydrateBatchFiles {
		end := min(start+hydrateBatchFiles, len(paths))
		batches = append(batches, paths[start:end])
	}

	var mu sync.Mutex
	var fetchSubBatch func(context.Context, []string) error
	fetchSubBatch = func(bctx context.Context, sub []string) error {
		if len(sub) == 0 {
			return nil
		}
		res, err := repo.ReadFiles(bctx, &corev1.ReadFilesRequest{
			CommitId: commit.Id,
			Paths:    sub,
		}, grpc.UseCompressor(gzip.Name))
		if err == nil {
			files := res.GetFiles()
			mu.Lock()
			if len(files) == len(sub) {
				for i, p := range sub {
					out[p] = diffutil.File{Path: p, Exists: true, Data: files[i].GetData()}
				}
			} else {
				byPath := make(map[string][]byte, len(files))
				for _, f := range files {
					byPath[f.GetPath()] = f.GetData()
				}
				for _, p := range sub {
					if data, ok := byPath[p]; ok {
						out[p] = diffutil.File{Path: p, Exists: true, Data: data}
					} else {
						out[p] = diffutil.File{Path: p}
					}
				}
			}
			mu.Unlock()
			return nil
		}
		code := grpcstatus.Code(err)
		if code != codes.NotFound && code != codes.ResourceExhausted && code != codes.Unimplemented {
			return err
		}
		if len(sub) > 8 && code != codes.Unimplemented {
			mid := len(sub) / 2
			if err := fetchSubBatch(bctx, sub[:mid]); err != nil {
				return err
			}
			return fetchSubBatch(bctx, sub[mid:])
		}
		for _, p := range sub {
			data, ok, singleErr := fetchSingleRemoteTarget(bctx, repo, blob, nil, cache, commit.Id, remoteReadTarget{Path: p})
			if singleErr != nil {
				return singleErr
			}
			mu.Lock()
			if ok {
				out[p] = diffutil.File{Path: p, Exists: true, Data: data}
			} else {
				out[p] = diffutil.File{Path: p}
			}
			mu.Unlock()
		}
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(hydrateBatchWorkers)
	for _, b := range batches {
		batchPaths := b
		group.Go(func() error {
			return fetchSubBatch(groupCtx, batchPaths)
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
