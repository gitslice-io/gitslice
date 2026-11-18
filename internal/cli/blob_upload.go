package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding/gzip"
	grpcstatus "google.golang.org/grpc/status"
)

type blobAttachStats struct {
	RequiredBlobs  int
	AvailableBlobs int
	UploadedBlobs  int
	UploadedBytes  int64
	UploadWorkers  int
	StatusDuration time.Duration
	UploadDuration time.Duration
}

func defaultUploadConcurrency() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		return 4
	}
	if n > 16 {
		return 16
	}
	return n
}

func defaultCaptureHashConcurrency() int {
	n := runtime.NumCPU() / 2
	if n < 2 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}

func boundedCaptureHashConcurrency(value, itemCount int) int {
	if itemCount <= 0 {
		return 0
	}
	if value <= 0 {
		value = defaultCaptureHashConcurrency()
	}
	if value > itemCount {
		return itemCount
	}
	return value
}

func boundedUploadConcurrency(value, itemCount int) int {
	if itemCount <= 0 {
		return 1
	}
	if value <= 0 {
		value = defaultUploadConcurrency()
	}
	if value > itemCount {
		return itemCount
	}
	return value
}

func uploadLocalBlob(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, file localUploadFile) (*corev1.UploadBlobResponse, error) {
	f, err := os.Open(file.LocalPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > blobStreamThresholdBytes {
		return uploadBlobReader(ctx, blobClient, sliceRef, file.ContentHash, info.Size(), f)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return uploadBlobBytes(ctx, blobClient, sliceRef, file.ContentHash, data)
}

func attachBlobIDs(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, edits []*corev1.FileEdit) error {
	_, err := attachBlobIDsWithStats(ctx, blobClient, sliceRef, cache, edits, defaultUploadConcurrency())
	return err
}

func attachBlobIDsWithStats(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, edits []*corev1.FileEdit, concurrency int) (blobAttachStats, error) {
	return attachBlobIDsFromFilesWithStats(ctx, blobClient, sliceRef, cache, edits, nil, concurrency)
}

func attachBlobIDsFromFilesWithStats(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, edits []*corev1.FileEdit, workingFiles map[string]workingFile, concurrency int) (blobAttachStats, error) {
	return NewTransferManager(nil, blobClient, sliceRef, cache).AttachAndUploadBlobs(ctx, edits, nil, workingFiles, concurrency)
}

func uploadCachedBlobs(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, hashes []string, concurrency int) (map[string]*corev1.UploadBlobResponse, error) {
	return uploadCachedBlobsWithFallback(ctx, blobClient, sliceRef, cache, hashes, nil, concurrency)
}

func ensureCachedBlobFromWorkingFile(cache *clientcache.ObjectCache, hash string, filesByHash map[string]workingFile) error {
	if cache.Exists(hash) {
		return nil
	}
	wf, ok := filesByHash[hash]
	if !ok || wf.AbsPath == "" {
		return nil
	}
	var cached clientcache.Object
	var err error
	if isSymlinkMode(wf.Mode) {
		link, lerr := os.Readlink(wf.AbsPath)
		if lerr != nil {
			return fmt.Errorf("read symlink %s: %w", wf.AbsPath, lerr)
		}
		cached, err = cache.PutBytes([]byte(link))
	} else {
		cached, err = cache.PutFile(wf.AbsPath)
	}
	if err != nil {
		return fmt.Errorf("cache workspace file %s: %w", wf.AbsPath, err)
	}
	if cached.ContentHash != hash {
		return fmt.Errorf("workspace file %s changed before upload: got hash %s, want %s", wf.AbsPath, cached.ContentHash, hash)
	}
	return nil
}

func uploadCachedBlobsWithFallback(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, hashes []string, filesByHash map[string]workingFile, concurrency int) (map[string]*corev1.UploadBlobResponse, error) {
	blobs := make([]pendingBlobUpload, 0, len(hashes))
	for _, hash := range hashes {
		if err := ensureCachedBlobFromWorkingFile(cache, hash, filesByHash); err != nil {
			return nil, err
		}
		cachePath, err := cache.Path(hash)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(cachePath)
		if err != nil {
			return nil, fmt.Errorf("stat cached object %s: %w", hash, err)
		}
		blobs = append(blobs, pendingBlobUpload{
			hash: hash,
			size: info.Size(),
			read: func() ([]byte, error) { return cache.Read(hash) },
			uploadOne: func() (*corev1.UploadBlobResponse, error) {
				return uploadCachedBlob(ctx, blobClient, sliceRef, cache, hash)
			},
		})
	}
	return uploadBlobsInBatches(ctx, blobClient, sliceRef, blobs, concurrency)
}

const uploadBatchBytes = 4 << 20

// uploadBatchBlobs is how many blobs one UploadBlobs call takes at most (a
// variable so tests can make batches small).
var uploadBatchBlobs = 256

// pendingBlobUpload is one blob for uploadBlobsInBatches: read gives a small
// blob's bytes for a batch; uploadOne uploads it alone (large blobs, or a
// server without UploadBlobs).
type pendingBlobUpload struct {
	hash      string
	size      int64
	read      func() ([]byte, error)
	uploadOne func() (*corev1.UploadBlobResponse, error)
}

// uploadBlobsInBatches uploads small blobs a batch per UploadBlobs call and
// large ones one at a time, up to concurrency calls at once, so a capture
// does not pay a round trip and an authorization per file
// (design/28_benchmarks.md). It returns what was uploaded, and the first
// error.
func uploadBlobsInBatches(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, blobs []pendingBlobUpload, concurrency int) (map[string]*corev1.UploadBlobResponse, error) {
	uploaded := make(map[string]*corev1.UploadBlobResponse, len(blobs))
	if len(blobs) == 0 {
		return uploaded, nil
	}
	var jobs [][]pendingBlobUpload
	var batch []pendingBlobUpload
	var batchBytes int64
	for _, blob := range blobs {
		if blob.size > blobStreamThresholdBytes {
			jobs = append(jobs, []pendingBlobUpload{blob})
			continue
		}
		if len(batch) > 0 && (len(batch) >= uploadBatchBlobs || batchBytes+blob.size > uploadBatchBytes) {
			jobs = append(jobs, batch)
			batch, batchBytes = nil, 0
		}
		batch = append(batch, blob)
		batchBytes += blob.size
	}
	if len(batch) > 0 {
		jobs = append(jobs, batch)
	}

	progress := startProgress(ctx, "uploading", "files", len(blobs))
	defer progress.Finish()
	var mu sync.Mutex
	record := func(hash string, response *corev1.UploadBlobResponse) {
		mu.Lock()
		uploaded[hash] = response
		mu.Unlock()
		progress.Add(1)
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(boundedUploadConcurrency(concurrency, len(jobs)))
	for _, job := range jobs {
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			if len(job) == 1 && job[0].size > blobStreamThresholdBytes {
				response, err := job[0].uploadOne()
				if err != nil {
					return err
				}
				record(job[0].hash, response)
				return nil
			}
			req := &corev1.UploadBlobsRequest{Slice: sliceRef, Blobs: make([]*corev1.UploadBlobsEntry, len(job))}
			for i, blob := range job {
				data, err := blob.read()
				if err != nil {
					return err
				}
				req.Blobs[i] = &corev1.UploadBlobsEntry{ContentHash: blob.hash, Data: data}
			}
			res, err := blobClient.UploadBlobs(groupCtx, req, grpc.UseCompressor(gzip.Name))
			if err != nil {
				if grpcstatus.Code(err) == codes.Unimplemented {
					for _, blob := range job {
						response, oneErr := blob.uploadOne()
						if oneErr != nil {
							return oneErr
						}
						record(blob.hash, response)
					}
					return nil
				}
				return err
			}
			if len(res.GetBlobs()) != len(job) {
				return fmt.Errorf("server recorded %d of %d blobs", len(res.GetBlobs()), len(job))
			}
			for i, blob := range job {
				record(blob.hash, res.GetBlobs()[i])
			}
			return nil
		})
	}
	return uploaded, group.Wait()
}

func uploadCachedBlob(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, hash string) (*corev1.UploadBlobResponse, error) {
	cachePath, err := cache.Path(hash)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		return nil, fmt.Errorf("stat cached object %s: %w", hash, err)
	}
	if info.Size() > blobStreamThresholdBytes {
		f, err := cache.Open(hash)
		if err != nil {
			return nil, fmt.Errorf("open cached object %s: %w", hash, err)
		}
		defer f.Close()
		return uploadBlobReader(ctx, blobClient, sliceRef, hash, info.Size(), f)
	}
	data, err := cache.Read(hash)
	if err != nil {
		return nil, fmt.Errorf("read cached object %s: %w", hash, err)
	}
	return uploadBlobBytes(ctx, blobClient, sliceRef, hash, data)
}
