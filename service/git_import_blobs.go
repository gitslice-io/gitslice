package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Import commits can touch hundreds of files. Uploading their blobs to the
// object store one at a time made a single ~200-file commit spend minutes in
// serial round-trips to remote object storage (R2), long enough to trip stream
// deadlines. Upload concurrently with a bounded worker pool, and give each
// PutObject its own timeout + a few retries so one stalled connection fails
// fast and recovers instead of hanging the whole import.
const (
	importBlobUploadConcurrency = 16
	importBlobUploadTimeout     = 30 * time.Second
	importBlobUploadAttempts    = 3
)

// uploadImportBlobs stores the new content of a commit's edits. A file the
// import already uploaded for an earlier commit (snapshot marks it Reused) is
// skipped, and so is content the server already holds: a commit that moves
// files uploads nothing, and an attempt that was cut off keeps what it
// stored. onProgress, if set, is told how many of the new files are stored.
func (s *RepositoryService) uploadImportBlobs(ctx context.Context, sliceID string, edits []*corev1.FileEdit, snapshot importSnapshot, onProgress func(done, total int)) error {
	type importBlob struct {
		key         string
		blobID      string
		contentHash string
		size        int64
		data        []byte
	}
	// Deduplicate by content hash: many paths in a commit can share identical
	// content, and the object key is content-addressed.
	seen := make(map[string]struct{})
	var blobs []importBlob
	for _, edit := range edits {
		if edit.Op == "delete" {
			continue
		}
		file, ok := snapshot[edit.Path]
		if !ok {
			return fmt.Errorf("missing import file for %s", edit.Path)
		}
		if file.Reused {
			continue
		}
		if _, dup := seen[file.ContentHash]; dup {
			continue
		}
		seen[file.ContentHash] = struct{}{}
		blobs = append(blobs, importBlob{
			key:         filesystem.BlobKey(file.ContentHash),
			blobID:      file.BlobID,
			contentHash: file.ContentHash,
			size:        file.Size,
			data:        file.Data,
		})
	}
	if len(blobs) == 0 {
		return nil
	}

	// Content the server holds already needs no upload.
	hashes := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		hashes = append(hashes, blob.contentHash)
	}
	stored, err := s.storedContentHashes(ctx, hashes)
	if err != nil {
		return err
	}
	var missing []importBlob
	for _, blob := range blobs {
		if _, ok := stored[blob.contentHash]; !ok {
			missing = append(missing, blob)
		}
	}

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
		onProgress(int(done.Load()), len(missing))
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(importBlobUploadConcurrency)
	for _, blob := range missing {
		blob := blob
		group.Go(func() error {
			if err := s.putImportBlob(groupCtx, blob.key, blob.data); err != nil {
				return err
			}
			if err := s.Blobs.Upsert(groupCtx, blob.blobID, blob.contentHash, blob.size, blob.key); err != nil {
				return err
			}
			done.Add(1)
			report(false)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	if len(missing) > 0 {
		report(true)
	}
	contentHashes := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		contentHashes = append(contentHashes, blob.contentHash)
	}
	return s.Blobs.AssociateSlices(ctx, sliceID, contentHashes)
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
