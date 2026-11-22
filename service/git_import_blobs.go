package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Import commits can touch thousands of files totaling hundreds of megabytes.
// Uploading blobs to the object store one at a time made large commits spend
// minutes in serial round-trips to remote object storage (R2), while buffering
// every blob in a commit in memory before uploading blew past Cloud Run's
// memory budget. Stream blobs from git in bounded-byte batches, upload each
// batch concurrently with a bounded worker pool, upsert blob metadata in bulk,
// and drop each batch's payload bytes before reading the next batch.
const (
	importBlobUploadConcurrency = 16
	importBlobUploadTimeout     = 30 * time.Second
	importBlobUploadAttempts    = 3
)

// importUploadError distinguishes storage/object-store failures during streaming
// import from git read failures so transient upload errors remain retryable.
type importUploadError struct {
	err error
}

func (e *importUploadError) Error() string { return e.err.Error() }
func (e *importUploadError) Unwrap() error { return e.err }

// streamAndUploadImportBlobs reads the commit's unseen git blobs in bounded
// memory batches, uploads any content the server does not already hold, records
// blob metadata and slice associations, and fills BlobID/ContentHash/Size on
// every entry of snapshot without keeping file bytes in memory.
func (s *RepositoryService) streamAndUploadImportBlobs(
	ctx context.Context,
	repoDir, sliceID string,
	snapshot importSnapshot,
	blobIDs []string,
	known knownBlobs,
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

	resolved := make(map[string]importFile, len(toRead))
	batchCap := maxGitImportBlobBatchCount
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
		stored, err := s.storedContentHashes(ctx, hashes)
		if err != nil {
			return &importUploadError{err: err}
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
		group.SetLimit(importBlobUploadConcurrency)
		for _, blob := range missing {
			blob := blob
			group.Go(func() error {
				if err := s.putImportBlob(groupCtx, blob.key, blob.data); err != nil {
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
			if err := s.Blobs.UpsertMany(ctx, uploaded); err != nil && groupErr == nil {
				return &importUploadError{err: err}
			}
		}
		if groupErr != nil {
			return &importUploadError{err: groupErr}
		}
		if err := s.Blobs.AssociateSlices(ctx, sliceID, hashes); err != nil {
			return &importUploadError{err: err}
		}
		for _, blob := range batch {
			meta := importFile{
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
		batchByteLimit := maxGitImportBlobBatchBytes
		batchCountLimit := maxGitImportBlobBatchCount
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
			key:         filesystem.BlobKey(contentHash),
			blobID:      blobID,
			contentHash: contentHash,
			size:        int64(len(data)),
			data:        data,
		})
		batchBytes += int64(len(data))
		return nil
	}

	if err := streamGitBlobContents(ctx, repoDir, toRead, beforeRead, onBlob); err != nil {
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

// storedContentHashes returns which of the content hashes the server holds
// as available blobs. A blob is recorded only after its object is stored.
func (s *RepositoryService) storedContentHashes(ctx context.Context, hashes []string) (map[string]struct{}, error) {
	const chunk = 1000
	stored := make(map[string]struct{}, len(hashes))
	for start := 0; start < len(hashes); start += chunk {
		end := min(start+chunk, len(hashes))
		records, err := s.Blobs.GetByContentHash(ctx, hashes[start:end])
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

// putImportBlob uploads a single import blob, bounding each attempt with its own
// timeout and retrying transient failures with a short backoff.
func (s *RepositoryService) putImportBlob(ctx context.Context, key string, data []byte) error {
	var err error
	for attempt := 0; attempt < importBlobUploadAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 250 * time.Millisecond):
			}
		}
		putCtx, cancel := context.WithTimeout(ctx, importBlobUploadTimeout)
		err = s.ObjectStore.Put(putCtx, key, bytes.NewReader(data))
		cancel()
		if err == nil {
			return nil
		}
		// Stop retrying when the parent context is done (the import was
		// canceled or another upload failed).
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}

func (s *RepositoryService) createImportPatchset(ctx context.Context, subjectID string, slice *corev1.Slice, targetRef, baseCommitID, message, description string, edits []*corev1.FileEdit) (*corev1.Patchset, error) {
	validation, err := s.validator.validateFileEdits(ctx, slice, baseCommitID, "", edits, true)
	if err != nil {
		return nil, err
	}
	cs, err := s.Changesets.Create(ctx, subjectID, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.Ref,
		TargetRef:      targetRef,
		BaseCommitId:   baseCommitID,
		Title:          message,
		Description:    description,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	patchset, err := s.Changesets.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId:       baseCommitID,
		Author:             subjectID,
		ChangedPaths:       validation.AffectedPaths,
		FileEdits:          edits,
		Coverage:           validation.Coverage,
		SubmitRequirements: validation.SubmitRequirements,
		PathBases:          validation.PathBases,
		ReadSet:            validation.ReadSet,
		WriteSet:           validation.WriteSet,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return patchset, nil
}

func (s *RepositoryService) waitForImportPublished(ctx context.Context, changesetID string, changedFileCount int) (string, error) {
	deadline := time.Now().Add(importPublishTimeout(changedFileCount))
	for {
		cs, err := s.Changesets.Get(ctx, changesetID)
		if err != nil {
			return "", err
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			// Only wait for the commit to be published (the ref advances so the
			// next commit can chain). The derived-index outbox is NOT drained
			// per commit — that work is redundant per commit and is drained once
			// after the whole import (see drainImportDerivedIndexes), keeping the
			// per-commit critical path cheap for deep imports.
			return cs.CommitId, nil
		}
		if _, err := s.Changesets.PublishPending(ctx, 128); err != nil && !errors.Is(err, storage.ErrConflict) {
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

// importPublishTimeout bounds how long a single imported commit may take to
// publish. The per-commit publish work itself is fast (a few seconds), but it
// shares the target ref's publish lock with the background publisher, so an
// occasional remote object-store latency spike can stall one commit for a
// minute or more. Use a generous floor so transient slowness doesn't fail an
// otherwise-healthy import, scaling modestly with the change size and capped to
// avoid hanging forever on a genuinely stuck commit.
func importPublishTimeout(changedFileCount int) time.Duration {
	timeout := 3 * time.Minute
	if changedFileCount > 0 {
		timeout += time.Duration(changedFileCount/50) * time.Second
	}
	if timeout > 30*time.Minute {
		return 30 * time.Minute
	}
	return timeout
}

// requireImportSubmitApprovals applies the submit rules that live outside the
// store to an imported commit, as SubmitChangeset does through submitExtras:
// a change under .gitslice needs an owner or admin of the account, and a
// change to paths with owners (.gitslice/owners.yaml) needs their approval.
// An import has no approvals, so the importer must hold them; otherwise the
// import stops before this commit, and its draft changeset stays open for
// review.
func (s *RepositoryService) requireImportSubmitApprovals(ctx context.Context, changesetID, headCommitID string) error {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return grpcError(err)
	}
	core := s.changesetsCore
	if core == nil {
		core = &changesetCore{
			Auth:        s.Auth,
			Blobs:       s.Blobs,
			Changesets:  s.Changesets,
			Repository:  s.Repository,
			Slices:      s.Slices,
			ObjectStore: s.ObjectStore,
			validator:   s.validator,
		}
	}
	rules := core.changesets()
	if err := rules.requireGitsliceApproval(ctx, cs, headCommitID); err != nil {
		return importRequirementError(cs, err)
	}
	if err := rules.requirePathOwnersApproval(ctx, cs, headCommitID); err != nil {
		return importRequirementError(cs, err)
	}
	return nil
}

func importRequirementError(cs *corev1.Changeset, err error) error {
	label := firstNonEmptyImport(storage.ShortChangesetID(cs.GetId()), cs.GetId())
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK && st.Code() != codes.Unknown {
		return status.Errorf(st.Code(), "import stopped: %s (changeset %s is left open for approval)", st.Message(), label)
	}
	return status.Errorf(codes.FailedPrecondition, "import stopped: %v (changeset %s is left open for approval)", err, label)
}

func firstNonEmptyImport(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
