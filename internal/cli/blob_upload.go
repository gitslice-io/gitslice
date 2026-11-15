package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
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

func remoteBlobRecords(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, files []localUploadFile) (map[string]*corev1.BlobRecord, error) {
	seen := map[string]struct{}{}
	hashes := make([]string, 0, len(files))
	for _, file := range files {
		if file.ContentHash == "" {
			return nil, fmt.Errorf("missing content hash for %s", file.LocalPath)
		}
		if _, ok := seen[file.ContentHash]; ok {
			continue
		}
		seen[file.ContentHash] = struct{}{}
		hashes = append(hashes, file.ContentHash)
	}
	records := map[string]*corev1.BlobRecord{}
	const batchSize = 512
	for start := 0; start < len(hashes); start += batchSize {
		end := start + batchSize
		if end > len(hashes) {
			end = len(hashes)
		}
		res, err := blobClient.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: hashes[start:end], Slice: sliceRef})
		if err != nil {
			return nil, err
		}
		for _, record := range res.Blobs {
			records[record.ContentHash] = record
		}
	}
	return records, nil
}

func uploadMissingLocalBlobs(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, missing map[string]localUploadFile, concurrency int) (map[string]*corev1.BlobRecord, error) {
	hashes := make([]string, 0, len(missing))
	for hash := range missing {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	blobs := make([]pendingBlobUpload, 0, len(hashes))
	for _, hash := range hashes {
		file := missing[hash]
		blobs = append(blobs, pendingBlobUpload{
			hash: hash,
			size: file.Size,
			read: func() ([]byte, error) { return os.ReadFile(file.LocalPath) },
			uploadOne: func() (*corev1.UploadBlobResponse, error) {
				return uploadLocalBlob(ctx, blobClient, sliceRef, file)
			},
		})
	}
	uploaded, err := uploadBlobsInBatches(ctx, blobClient, sliceRef, blobs, concurrency)
	records := make(map[string]*corev1.BlobRecord, len(uploaded))
	for hash, upload := range uploaded {
		if upload == nil {
			continue
		}
		records[hash] = &corev1.BlobRecord{
			Id:          upload.BlobId,
			ContentHash: upload.ContentHash,
			Size:        upload.Size,
			State:       "present",
		}
	}
	return records, err
}

func uploadLocalBlob(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, file localUploadFile) (*corev1.UploadBlobResponse, error) {
	if file.Size > blobStreamThresholdBytes {
		f, err := os.Open(file.LocalPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return uploadBlobReader(ctx, blobClient, sliceRef, file.ContentHash, file.Size, f)
	}
	data, err := os.ReadFile(file.LocalPath)
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
	var stats blobAttachStats
	hashSet := map[string]struct{}{}
	for _, edit := range edits {
		if edit == nil || edit.Op == "delete" || edit.Op == "rename" || edit.BlobId != "" || edit.ContentHash == "" {
			continue
		}
		hashSet[edit.ContentHash] = struct{}{}
	}
	if len(hashSet) == 0 {
		return stats, nil
	}

	hashes := make([]string, 0, len(hashSet))
	for hash := range hashSet {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	stats.RequiredBlobs = len(hashes)

	blobIDs := map[string]string{}
	statusStarted := time.Now()
	const statusBatchSize = 512
	for start := 0; start < len(hashes); start += statusBatchSize {
		end := start + statusBatchSize
		if end > len(hashes) {
			end = len(hashes)
		}
		status, err := blobClient.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: hashes[start:end], Slice: sliceRef})
		if err != nil {
			stats.StatusDuration = time.Since(statusStarted)
			return stats, err
		}
		for _, record := range status.Blobs {
			if record.Id != "" && record.State == "available" {
				blobIDs[record.ContentHash] = record.Id
			}
		}
	}
	stats.StatusDuration = time.Since(statusStarted)
	stats.AvailableBlobs = len(blobIDs)

	missing := make([]string, 0, len(hashes)-len(blobIDs))
	for _, hash := range hashes {
		if blobIDs[hash] != "" {
			continue
		}
		missing = append(missing, hash)
	}
	if len(missing) > 0 {
		stats.UploadWorkers = boundedUploadConcurrency(concurrency, len(missing))
	}
	uploadStarted := time.Now()
	uploaded, err := uploadCachedBlobs(ctx, blobClient, sliceRef, cache, missing, stats.UploadWorkers)
	stats.UploadDuration = time.Since(uploadStarted)
	for hash, response := range uploaded {
		if response == nil || response.BlobId == "" {
			continue
		}
		blobIDs[hash] = response.BlobId
		stats.UploadedBlobs++
		stats.UploadedBytes += response.Size
	}
	if err != nil {
		return stats, err
	}

	for _, edit := range edits {
		if edit == nil || edit.Op == "delete" || edit.Op == "rename" || edit.BlobId != "" {
			continue
		}
		blobID := blobIDs[edit.ContentHash]
		if blobID == "" {
			return stats, fmt.Errorf("blob upload did not return blob id for content hash %s", edit.ContentHash)
		}
		edit.BlobId = blobID
	}
	return stats, nil
}

func uploadCachedBlobs(ctx context.Context, blobClient corev1.BlobServiceClient, sliceRef *corev1.SliceRef, cache *clientcache.ObjectCache, hashes []string, concurrency int) (map[string]*corev1.UploadBlobResponse, error) {
	blobs := make([]pendingBlobUpload, 0, len(hashes))
	for _, hash := range hashes {
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
