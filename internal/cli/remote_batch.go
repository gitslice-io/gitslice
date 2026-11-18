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

// prefetchRemoteFilesToCache fetches uncached files at commitID into cache
// through a TransferManager (bounded concurrent ReadFiles batches, large files
// streamed via ReadBlobStream). It returns any fetched file bytes keyed by path
// for targets whose ContentHash was unknown in advance.
func prefetchRemoteFilesToCache(
	ctx context.Context,
	repo corev1.RepositoryServiceClient,
	blob corev1.BlobServiceClient,
	sliceRef *corev1.SliceRef,
	cache *clientcache.ObjectCache,
	commitID string,
	targets []remoteReadTarget,
) (map[string][]byte, error) {
	if repo == nil {
		return map[string][]byte{}, nil
	}
	res, err := NewTransferManager(repo, blob, sliceRef, cache).FetchFilesToCache(ctx, TransferFetchRequest{
		CommitID:             commitID,
		Targets:              targets,
		VerifyExistingHashes: true,
	})
	if err != nil {
		return nil, err
	}
	return res.UnhashedData, nil
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
	return NewTransferManager(repo, blob, sliceRef, cache).fetchSingleTarget(ctx, commitID, "", target)
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
