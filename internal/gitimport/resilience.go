package gitimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// HeartbeatEvery is how often an import tells its client it is still
	// working.
	HeartbeatEvery = 10 * time.Second
	// CommitTimeout bounds one commit's work after the client that asked for
	// it went away.
	CommitTimeout = 20 * time.Minute

	// BlobUploadConcurrency bounds concurrent object-store writes per batch.
	BlobUploadConcurrency = 16
	// BlobUploadTimeout bounds a single object-store put attempt.
	BlobUploadTimeout = 30 * time.Second
	// BlobUploadAttempts is the maximum number of attempts per blob upload.
	BlobUploadAttempts = 3
)

// Progress sends an import's progress to its client. It is safe for concurrent
// use, and a client that has gone away is not an error: the import goes on
// with the commit it is publishing and stops at the next one.
type Progress struct {
	mu   sync.Mutex
	send func(*corev1.ImportGitRepositoryProgress) error
	gone bool
	last corev1.ImportGitRepositoryProgress
	// HeartbeatEvery defaults to HeartbeatEvery; tests shorten it.
	HeartbeatEvery time.Duration
}

// NewProgress wraps a progress callback in a concurrency-safe Progress sink.
func NewProgress(send func(*corev1.ImportGitRepositoryProgress) error) *Progress {
	return &Progress{send: send, HeartbeatEvery: HeartbeatEvery}
}

// Emit forwards event to the client unless it has already disconnected.
func (p *Progress) Emit(event *corev1.ImportGitRepositoryProgress) error {
	if p == nil || p.send == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Phase != "working" {
		p.last.Phase, p.last.Message = event.Phase, event.Message
		p.last.Current, p.last.Total = event.Current, event.Total
		p.last.GitCommitId = event.GitCommitId
	}
	if p.gone {
		return nil
	}
	if err := p.send(event); err != nil {
		p.gone = true
	}
	return nil
}

// Heartbeat tells the client every HeartbeatEvery that the import is still
// going, as "working" with the position of the last real event. It stops when
// the returned func is called.
func (p *Progress) Heartbeat() (stop func()) {
	if p == nil || p.send == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(p.HeartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				p.mu.Lock()
				event := &corev1.ImportGitRepositoryProgress{
					Phase:       "working",
					Message:     p.last.Message,
					Current:     p.last.Current,
					Total:       p.last.Total,
					GitCommitId: p.last.GitCommitId,
				}
				if !p.gone {
					if err := p.send(event); err != nil {
						p.gone = true
					}
				}
				p.mu.Unlock()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// EmitProgress invokes progress(event) when progress is non-nil.
func EmitProgress(progress func(*corev1.ImportGitRepositoryProgress) error, event *corev1.ImportGitRepositoryProgress) error {
	if progress == nil {
		return nil
	}
	return progress(event)
}

// Flights makes imports of the same source into the same place run one at a
// time. A client that reconnects while the server is still finishing the
// commit its previous connection started waits for it, then resumes after it.
type Flights struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// Acquire waits for the import named key, calling waiting every HeartbeatEvery
// meanwhile, and returns the func that releases it.
func (f *Flights) Acquire(ctx context.Context, key string, waiting func()) (func(), error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]chan struct{}{}
	}
	lock := f.locks[key]
	if lock == nil {
		lock = make(chan struct{}, 1)
		f.locks[key] = lock
	}
	f.mu.Unlock()
	ticker := time.NewTicker(HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case lock <- struct{}{}:
			return func() { <-lock }, nil
		case <-ticker.C:
			if waiting != nil {
				waiting()
			}
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
}

// WorkContext returns a context that keeps request values without request
// cancellation, bounded by CommitTimeout, so a client drop does not leave an
// in-progress commit half done.
func WorkContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), CommitTimeout)
}

// ReadError maps a git read failure to a gRPC status: InvalidArgument unless
// the work was cut short or exceeded resource limits.
func ReadError(err error, message string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.Errorf(codes.Unavailable, "%s: %v", message, err)
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.ResourceExhausted {
		return status.Errorf(codes.ResourceExhausted, "%s: %s", message, st.Message())
	}
	return status.Errorf(codes.InvalidArgument, "%s: %v", message, err)
}

// UploadError distinguishes storage/object-store failures during streaming
// import from git read failures so transient upload errors remain retryable.
type UploadError struct {
	Err error
}

func (e *UploadError) Error() string { return e.Err.Error() }
func (e *UploadError) Unwrap() error { return e.Err }

// BlobPutter is the narrow object-store interface needed to upload import blobs.
type BlobPutter interface {
	Put(ctx context.Context, key string, r io.Reader) error
}

// StreamAndUploadBlobs reads the commit's unseen git blobs in bounded memory
// batches, uploads any content the server does not already hold, records blob
// metadata and slice associations, and fills BlobID/ContentHash/Size on every
// entry of snapshot without keeping file bytes in memory.
func StreamAndUploadBlobs(
	ctx context.Context,
	blobs storage.BlobStore,
	objects BlobPutter,
	repoDir, sliceID string,
	snapshot Snapshot,
	blobIDs []string,
	known KnownBlobs,
	onProgress func(done, total int),
) error {
	type pendingBlob struct {
		gitBlobID   string
		key         string
		blobID      string
		contentHash string
		size        int64
		data        []byte
	}
	toRead := make([]string, 0, len(blobIDs))
	for _, id := range blobIDs {
		if _, ok := known[id]; !ok {
			toRead = append(toRead, id)
		}
	}

	resolved := make(map[string]File, len(toRead))
	batchCap := MaxBlobBatchCount
	if batchCap <= 0 {
		batchCap = 1
	}
	if len(toRead) < batchCap {
		batchCap = len(toRead)
	}
	batch := make([]pendingBlob, 0, batchCap)
	var batchBytes int64

	var done atomic.Int64
	var lastReport atomic.Int64
	report := func(force bool) {
		if onProgress == nil {
			return
		}
		now := time.Now().UnixNano()
		last := lastReport.Load()
		if !force && (now-last < int64(2*time.Second) || !lastReport.CompareAndSwap(last, now)) {
			return
		}
		onProgress(int(done.Load()), len(toRead))
	}

	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		seen := make(map[string]struct{}, len(batch))
		unique := make([]pendingBlob, 0, len(batch))
		hashes := make([]string, 0, len(batch))
		for _, blob := range batch {
			if _, dup := seen[blob.contentHash]; dup {
				continue
			}
			seen[blob.contentHash] = struct{}{}
			unique = append(unique, blob)
			hashes = append(hashes, blob.contentHash)
		}
		stored, err := storedContentHashes(ctx, blobs, hashes)
		if err != nil {
			return &UploadError{Err: err}
		}
		var missing []pendingBlob
		var alreadyStored int64
		for _, blob := range unique {
			if _, ok := stored[blob.contentHash]; ok {
				alreadyStored++
			} else {
				missing = append(missing, blob)
			}
		}
		if alreadyStored > 0 {
			done.Add(alreadyStored)
		}

		var uploadedMu sync.Mutex
		uploaded := make([]storage.BlobUpsert, 0, len(missing))
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(BlobUploadConcurrency)
		for _, blob := range missing {
			blob := blob
			group.Go(func() error {
				if err := putBlobWithRetry(groupCtx, objects, blob.key, blob.data); err != nil {
					return err
				}
				uploadedMu.Lock()
				uploaded = append(uploaded, storage.BlobUpsert{
					ID:              blob.blobID,
					ContentHash:     blob.contentHash,
					Size:            blob.size,
					StorageLocation: blob.key,
				})
				uploadedMu.Unlock()
				done.Add(1)
				report(false)
				return nil
			})
		}
		groupErr := group.Wait()
		if len(uploaded) > 0 && (groupErr == nil || ctx.Err() == nil) {
			if err := blobs.UpsertMany(ctx, uploaded); err != nil && groupErr == nil {
				return &UploadError{Err: err}
			}
		}
		if groupErr != nil {
			return &UploadError{Err: groupErr}
		}
		if err := blobs.AssociateSlices(ctx, sliceID, hashes); err != nil {
			return &UploadError{Err: err}
		}
		for _, blob := range batch {
			meta := File{
				BlobID:      blob.blobID,
				ContentHash: blob.contentHash,
				Size:        blob.size,
			}
			resolved[blob.gitBlobID] = meta
			if known != nil {
				known[blob.gitBlobID] = meta
			}
		}
		clear(batch)
		batch = batch[:0]
		batchBytes = 0
		report(false)
		return nil
	}

	beforeRead := func(size int64) error {
		batchByteLimit := MaxBlobBatchBytes
		batchCountLimit := MaxBlobBatchCount
		if len(batch) > 0 && ((batchByteLimit > 0 && batchBytes+size > batchByteLimit) || (batchCountLimit > 0 && len(batch) >= batchCountLimit)) {
			return flushBatch()
		}
		return nil
	}
	onBlob := func(gitBlobID string, data []byte) error {
		contentHash := objectid.RawContentHash(data)
		blobID := objectid.BlobID(data)
		batch = append(batch, pendingBlob{
			gitBlobID:   gitBlobID,
			key:         objectstore.BlobKey(contentHash),
			blobID:      blobID,
			contentHash: contentHash,
			size:        int64(len(data)),
			data:        data,
		})
		batchBytes += int64(len(data))
		return nil
	}

	if err := StreamBlobContents(ctx, repoDir, toRead, beforeRead, onBlob); err != nil {
		return err
	}
	if err := flushBatch(); err != nil {
		return err
	}
	if len(toRead) > 0 {
		report(true)
	}
	for p, file := range snapshot {
		if meta, ok := resolved[file.GitBlobID]; ok {
			file.BlobID, file.ContentHash, file.Size = meta.BlobID, meta.ContentHash, meta.Size
			snapshot[p] = file
			continue
		}
		if k, ok := known[file.GitBlobID]; ok {
			file.BlobID, file.ContentHash, file.Size, file.Reused = k.BlobID, k.ContentHash, k.Size, true
			snapshot[p] = file
			continue
		}
		return fmt.Errorf("missing git blob %s for %s", file.GitBlobID, file.GitPath)
	}
	return nil
}

func storedContentHashes(ctx context.Context, blobs storage.BlobStore, hashes []string) (map[string]struct{}, error) {
	const chunk = 1000
	stored := make(map[string]struct{}, len(hashes))
	for start := 0; start < len(hashes); start += chunk {
		end := min(start+chunk, len(hashes))
		records, err := blobs.GetByContentHash(ctx, hashes[start:end])
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if record.GetState() == "" || record.GetState() == "available" {
				stored[record.GetContentHash()] = struct{}{}
			}
		}
	}
	return stored, nil
}

func putBlobWithRetry(ctx context.Context, objects BlobPutter, key string, data []byte) error {
	var err error
	for attempt := 0; attempt < BlobUploadAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 250 * time.Millisecond):
			}
		}
		putCtx, cancel := context.WithTimeout(ctx, BlobUploadTimeout)
		err = objects.Put(putCtx, key, bytes.NewReader(data))
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}

// PublishTimeout bounds how long a single imported commit may take to publish.
func PublishTimeout(changedFileCount int) time.Duration {
	timeout := 3 * time.Minute
	if changedFileCount > 0 {
		timeout += time.Duration(changedFileCount/50) * time.Second
	}
	if timeout > 30*time.Minute {
		return 30 * time.Minute
	}
	return timeout
}

// WaitForPublished polls until changesetID reaches submitted status with a commit ID.
func WaitForPublished(ctx context.Context, changesets storage.ChangesetStore, changesetID string, changedFileCount int) (string, error) {
	deadline := time.Now().Add(PublishTimeout(changedFileCount))
	for {
		cs, err := changesets.Get(ctx, changesetID)
		if err != nil {
			return "", err
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs.CommitId, nil
		}
		if _, err := changesets.PublishPending(ctx, 128); err != nil && !errors.Is(err, storage.ErrConflict) {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for imported changeset %s to publish", changesetID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
