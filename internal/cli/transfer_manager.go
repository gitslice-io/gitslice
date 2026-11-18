package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
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

// TransferRepoClient is the batched-read subset of RepositoryServiceClient
// that TransferManager needs. Clients that also implement transferFileReader
// and transferPathResolver (the generated client does) get the single-file and
// oversized-file fallbacks.
type TransferRepoClient interface {
	ReadFiles(ctx context.Context, in *corev1.ReadFilesRequest, opts ...grpc.CallOption) (*corev1.ReadFilesResponse, error)
}

type transferFileReader interface {
	ReadFile(ctx context.Context, in *corev1.ReadFileRequest, opts ...grpc.CallOption) (*corev1.ReadFileResponse, error)
}

type transferPathResolver interface {
	ResolvePath(ctx context.Context, in *corev1.ResolvePathRequest, opts ...grpc.CallOption) (*corev1.ResolvePathResponse, error)
}

// TransferManager unifies batched ReadFiles / ReadBlobStream downloads into the
// client object cache and GetBlobStatus + UploadBlobs uploads across CLI commands.
type TransferManager struct {
	Repo     TransferRepoClient
	Blob     corev1.BlobServiceClient
	SliceRef *corev1.SliceRef
	Cache    *clientcache.ObjectCache
}

func NewTransferManager(
	repo TransferRepoClient,
	blob corev1.BlobServiceClient,
	sliceRef *corev1.SliceRef,
	cache *clientcache.ObjectCache,
) *TransferManager {
	return &TransferManager{
		Repo:     repo,
		Blob:     blob,
		SliceRef: sliceRef,
		Cache:    cache,
	}
}

type TransferFetchRequest struct {
	CommitID             string
	RootTreeID           string
	Targets              []remoteReadTarget
	SkipExistingCheck    bool
	VerifyExistingHashes bool
	Workers              int
}

type TransferFetchResult struct {
	UnhashedData map[string][]byte
	FetchedFiles int
	FetchedBytes int64
}

func (tm *TransferManager) FetchFilesToCache(ctx context.Context, req TransferFetchRequest) (TransferFetchResult, error) {
	res := TransferFetchResult{
		UnhashedData: map[string][]byte{},
	}
	if (req.CommitID == "" && req.RootTreeID == "") || len(req.Targets) == 0 || (tm.Repo == nil && tm.Blob == nil) {
		return res, nil
	}

	var large []remoteReadTarget
	var small []remoteReadTarget
	seenHashes := map[string]struct{}{}
	seenPaths := map[string]struct{}{}

	for _, t := range req.Targets {
		if t.Path == "" && t.ContentHash == "" {
			continue
		}
		if t.ContentHash != "" {
			if _, ok := seenHashes[t.ContentHash]; ok {
				continue
			}
			if !req.SkipExistingCheck && tm.Cache != nil && tm.Cache.Exists(t.ContentHash) {
				if req.VerifyExistingHashes {
					if _, ok, err := readVerifiedCachedBytes(tm.Cache, t.ContentHash); err == nil && ok {
						seenHashes[t.ContentHash] = struct{}{}
						continue
					}
				} else {
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

		if (t.Path == "" && t.ContentHash != "") || (t.Size > blobStreamThresholdBytes && tm.Blob != nil && t.ContentHash != "") {
			large = append(large, t)
			continue
		}
		small = append(small, t)
	}

	for _, t := range large {
		nBytes, fetched, err := tm.streamTargetToCache(ctx, req.CommitID, req.RootTreeID, t)
		if err != nil {
			return res, err
		}
		if fetched {
			res.FetchedFiles++
			res.FetchedBytes += nBytes
		}
	}

	if len(small) == 0 {
		return res, nil
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

	workers := req.Workers
	if workers <= 0 {
		workers = hydrateBatchWorkers
	}

	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(workers)
	for _, b := range batches {
		batchTargets := b
		group.Go(func() error {
			return tm.fetchBatch(groupCtx, req.CommitID, req.RootTreeID, batchTargets, &mu, &res)
		})
	}
	if err := group.Wait(); err != nil {
		return res, err
	}
	return res, nil
}

func (tm *TransferManager) fetchBatch(
	ctx context.Context,
	commitID, rootTreeID string,
	targets []remoteReadTarget,
	mu *sync.Mutex,
	out *TransferFetchResult,
) error {
	paths := make([]string, len(targets))
	for i, t := range targets {
		paths[i] = t.Path
	}
	res, err := tm.Repo.ReadFiles(ctx, &corev1.ReadFilesRequest{
		CommitId:   commitID,
		RootTreeId: rootTreeID,
		Paths:      paths,
		Slice:      tm.SliceRef,
	}, grpc.UseCompressor(gzip.Name))
	if err != nil {
		code := grpcstatus.Code(err)
		if code == codes.Unimplemented || code == codes.ResourceExhausted || code == codes.NotFound {
			for _, t := range targets {
				data, ok, singleErr := tm.fetchSingleTarget(ctx, commitID, rootTreeID, t)
				if singleErr != nil {
					return singleErr
				}
				if ok {
					mu.Lock()
					out.FetchedFiles++
					out.FetchedBytes += int64(len(data))
					if t.ContentHash == "" {
						out.UnhashedData[t.Path] = data
					}
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
			if _, err := putVerifiedCachedBytes(tm.Cache, t.Path, t.ContentHash, data); err != nil {
				return err
			}
			mu.Lock()
			out.FetchedFiles++
			out.FetchedBytes += int64(len(data))
			if t.ContentHash == "" {
				out.UnhashedData[t.Path] = data
			}
			mu.Unlock()
		}
		return nil
	}

	for i, t := range targets {
		data := files[i].GetData()
		if _, err := putVerifiedCachedBytes(tm.Cache, t.Path, t.ContentHash, data); err != nil {
			return err
		}
		mu.Lock()
		out.FetchedFiles++
		out.FetchedBytes += int64(len(data))
		if t.ContentHash == "" {
			out.UnhashedData[t.Path] = data
		}
		mu.Unlock()
	}
	return nil
}

func (tm *TransferManager) streamTargetToCache(
	ctx context.Context,
	commitID, rootTreeID string,
	target remoteReadTarget,
) (int64, bool, error) {
	if tm.Blob != nil && target.ContentHash != "" && tm.Cache != nil {
		stream, err := tm.Blob.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{
			Slice:       tm.SliceRef,
			ContentHash: target.ContentHash,
		})
		if err == nil {
			cached, putErr := tm.Cache.PutReader(newBlobStreamReader(stream))
			if putErr == nil {
				if cached.ContentHash != target.ContentHash {
					return 0, false, fmt.Errorf("content hash mismatch for %s: got %s, want %s", firstNonEmpty(target.Path, target.ContentHash), cached.ContentHash, target.ContentHash)
				}
				return cached.Size, true, nil
			}
			if grpcstatus.Code(putErr) != codes.NotFound && grpcstatus.Code(putErr) != codes.Unimplemented || target.Path == "" {
				return 0, false, putErr
			}
		} else if grpcstatus.Code(err) != codes.NotFound && grpcstatus.Code(err) != codes.Unimplemented || target.Path == "" {
			return 0, false, err
		}
	}
	if target.Path == "" {
		return 0, false, fmt.Errorf("cannot fetch hash-only target %s without BlobServiceClient", target.ContentHash)
	}
	data, ok, err := tm.fetchSingleTarget(ctx, commitID, rootTreeID, target)
	return int64(len(data)), ok, err
}

func (tm *TransferManager) fetchSingleTarget(
	ctx context.Context,
	commitID, rootTreeID string,
	target remoteReadTarget,
) ([]byte, bool, error) {
	if tm.Blob != nil && target.ContentHash != "" {
		data, err := readBlobStreamBytes(ctx, tm.Blob, tm.SliceRef, target.ContentHash, 0, 0)
		if err == nil {
			if _, verifyErr := putVerifiedCachedBytes(tm.Cache, target.Path, target.ContentHash, data); verifyErr != nil {
				return nil, false, verifyErr
			}
			return data, true, nil
		}
		if grpcstatus.Code(err) != codes.NotFound && grpcstatus.Code(err) != codes.Unimplemented {
			return nil, false, err
		}
	}
	reader, ok := tm.Repo.(transferFileReader)
	if !ok || tm.Repo == nil {
		return nil, false, fmt.Errorf("repository client cannot read %s", target.Path)
	}
	read, err := reader.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId:   commitID,
		RootTreeId: rootTreeID,
		Path:       target.Path,
		Slice:      tm.SliceRef,
	}, grpc.UseCompressor(gzip.Name))
	resolver, canResolve := tm.Repo.(transferPathResolver)
	if grpcstatus.Code(err) == codes.ResourceExhausted && tm.Blob != nil && canResolve {
		resolved, resErr := resolver.ResolvePath(ctx, &corev1.ResolvePathRequest{
			CommitId: commitID,
			Path:     target.Path,
			Slice:    tm.SliceRef,
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
		data, streamErr := readBlobStreamBytes(ctx, tm.Blob, tm.SliceRef, hash, 0, 0)
		if streamErr != nil {
			return nil, false, streamErr
		}
		if _, verifyErr := putVerifiedCachedBytes(tm.Cache, target.Path, expected, data); verifyErr != nil {
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
	if _, verifyErr := putVerifiedCachedBytes(tm.Cache, target.Path, target.ContentHash, data); verifyErr != nil {
		return nil, false, verifyErr
	}
	return data, true, nil
}

// BlobUploadSource describes how to read or stream a local/in-memory blob for upload.
type BlobUploadSource struct {
	Size       int64
	ReadBytes  func() ([]byte, error)
	OpenReader func() (io.ReadCloser, int64, error)
}

func BlobBytesSources(byHash map[string][]byte) map[string]BlobUploadSource {
	out := make(map[string]BlobUploadSource, len(byHash))
	for hash, data := range byHash {
		d := data
		out[hash] = BlobUploadSource{
			Size:      int64(len(d)),
			ReadBytes: func() ([]byte, error) { return d, nil },
			OpenReader: func() (io.ReadCloser, int64, error) {
				return io.NopCloser(bytes.NewReader(d)), int64(len(d)), nil
			},
		}
	}
	return out
}

func BlobLocalFileSources(files []localUploadFile) map[string]BlobUploadSource {
	out := make(map[string]BlobUploadSource, len(files))
	for _, f := range files {
		if f.ContentHash == "" {
			continue
		}
		if _, exists := out[f.ContentHash]; exists {
			continue
		}
		file := f
		out[file.ContentHash] = BlobUploadSource{
			Size:      file.Size,
			ReadBytes: func() ([]byte, error) { return os.ReadFile(file.LocalPath) },
			OpenReader: func() (io.ReadCloser, int64, error) {
				rc, err := os.Open(file.LocalPath)
				if err != nil {
					return nil, 0, err
				}
				info, err := rc.Stat()
				if err != nil {
					rc.Close()
					return nil, 0, err
				}
				return rc, info.Size(), nil
			},
		}
	}
	return out
}

// AttachAndUploadBlobs checks GetBlobStatus (State == "available"), uploads any
// missing blobs in compressed UploadBlobs batches (or UploadBlob streams for
// large blobs), and populates BlobId on every non-delete/non-rename FileEdit.
func (tm *TransferManager) AttachAndUploadBlobs(
	ctx context.Context,
	edits []*corev1.FileEdit,
	sources map[string]BlobUploadSource,
	workingFiles map[string]workingFile,
	concurrency int,
) (blobAttachStats, error) {
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
		end := min(start+statusBatchSize, len(hashes))
		status, err := tm.Blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{
			ContentHashes: hashes[start:end],
			Slice:         tm.SliceRef,
		})
		if err != nil {
			if grpcstatus.Code(err) == codes.Unimplemented {
				break
			}
			stats.StatusDuration = time.Since(statusStarted)
			return stats, err
		}
		for _, record := range status.GetBlobs() {
			if record.GetId() != "" && record.GetState() == "available" {
				blobIDs[record.GetContentHash()] = record.GetId()
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

	filesByHash := map[string]workingFile{}
	for _, edit := range edits {
		if edit == nil || edit.ContentHash == "" {
			continue
		}
		if wf, ok := workingFiles[edit.Path]; ok && wf.AbsPath != "" {
			if _, exists := filesByHash[edit.ContentHash]; !exists {
				filesByHash[edit.ContentHash] = wf
			}
		}
	}

	pending := make([]pendingBlobUpload, 0, len(missing))
	for _, hash := range missing {
		h := hash
		if src, ok := sources[h]; ok {
			pending = append(pending, pendingBlobUpload{
				hash: h,
				size: src.Size,
				read: src.ReadBytes,
				uploadOne: func() (*corev1.UploadBlobResponse, error) {
					return tm.uploadFromSource(ctx, h, src)
				},
			})
			continue
		}
		if tm.Cache == nil {
			return stats, fmt.Errorf("missing blob upload source and object cache for hash %s", h)
		}
		if err := ensureCachedBlobFromWorkingFile(tm.Cache, h, filesByHash); err != nil {
			return stats, err
		}
		cachePath, err := tm.Cache.Path(h)
		if err != nil {
			return stats, err
		}
		info, err := os.Stat(cachePath)
		if err != nil {
			return stats, fmt.Errorf("stat cached object %s: %w", h, err)
		}
		pending = append(pending, pendingBlobUpload{
			hash: h,
			size: info.Size(),
			read: func() ([]byte, error) { return tm.Cache.Read(h) },
			uploadOne: func() (*corev1.UploadBlobResponse, error) {
				return uploadCachedBlob(ctx, tm.Blob, tm.SliceRef, tm.Cache, h)
			},
		})
	}

	uploadStarted := time.Now()
	uploaded, err := uploadBlobsInBatches(ctx, tm.Blob, tm.SliceRef, pending, stats.UploadWorkers)
	stats.UploadDuration = time.Since(uploadStarted)
	for hash, response := range uploaded {
		if response == nil || response.GetBlobId() == "" {
			continue
		}
		blobIDs[hash] = response.GetBlobId()
		stats.UploadedBlobs++
		stats.UploadedBytes += response.GetSize()
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
			label := firstNonEmpty(edit.Path, edit.ContentHash)
			return stats, fmt.Errorf("blob upload did not return blob id for %s", label)
		}
		edit.BlobId = blobID
	}
	return stats, nil
}

func (tm *TransferManager) uploadFromSource(ctx context.Context, hash string, src BlobUploadSource) (*corev1.UploadBlobResponse, error) {
	if src.OpenReader != nil && src.Size > blobStreamThresholdBytes {
		rc, size, err := src.OpenReader()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		if size > blobStreamThresholdBytes {
			return uploadBlobReader(ctx, tm.Blob, tm.SliceRef, hash, size, rc)
		}
	}
	data, err := src.ReadBytes()
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > blobStreamThresholdBytes {
		return uploadBlobReader(ctx, tm.Blob, tm.SliceRef, hash, int64(len(data)), bytes.NewReader(data))
	}
	return uploadBlobBytes(ctx, tm.Blob, tm.SliceRef, hash, data)
}
