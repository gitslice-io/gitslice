package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *RepositoryService) ImportGitRepository(ctx context.Context, req *corev1.ImportGitRepositoryRequest) (*corev1.ImportGitRepositoryResponse, error) {
	return s.importGitRepository(ctx, req, nil)
}

func (s *RepositoryService) ImportGitRepositoryStream(req *corev1.ImportGitRepositoryRequest, stream corev1.RepositoryService_ImportGitRepositoryStreamServer) error {
	_, err := s.importGitRepository(stream.Context(), req, func(progress *corev1.ImportGitRepositoryProgress) error {
		return stream.Send(progress)
	})
	return err
}

func (s *RepositoryService) importGitRepository(ctx context.Context, req *corev1.ImportGitRepositoryRequest, progress func(*corev1.ImportGitRepositoryProgress) error) (*corev1.ImportGitRepositoryResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	mountPath, err := paths.Canonical(req.MountPath)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !paths.InAnyPrefix(slice.Definition.IncludedPaths, mountPath) {
		return nil, status.Errorf(codes.FailedPrecondition, "mount path %s is outside slice %s/%s", mountPath, slice.Ref.Account, slice.Ref.Slice)
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "shallow"
	}
	if mode != "shallow" && mode != "deep" {
		return nil, status.Error(codes.InvalidArgument, "import mode must be shallow or deep")
	}
	if req.MaxCommits < 0 {
		return nil, status.Error(codes.InvalidArgument, "max_commits must be non-negative")
	}
	if err := requireDefaultTargetRef(req.TargetRef); err != nil {
		return nil, err
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	source, localSource, err := validateImportSource(req.Source)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "cloning",
		Message: "cloning git repository",
	}); err != nil {
		return nil, err
	}
	repoDir, cleanup, err := cloneForImport(ctx, source, mode, int(req.MaxCommits), localSource)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "clone git repository: %v", err)
	}
	defer cleanup()
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "listing_commits",
		Message: "listing git commits",
	}); err != nil {
		return nil, err
	}
	gitCommits, err := selectedGitCommits(ctx, repoDir, mode, int(req.MaxCommits))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "list git commits: %v", err)
	}
	if len(gitCommits) == 0 {
		return nil, status.Error(codes.InvalidArgument, "git repository has no commits")
	}
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "listed_commits",
		Message: "listed git commits",
		Total:   int64(len(gitCommits)),
	}); err != nil {
		return nil, err
	}
	previous, err := s.currentMountedSnapshot(ctx, targetRef, mountPath)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &corev1.ImportGitRepositoryResponse{
		Source:    source,
		MountPath: mountPath,
		Mode:      mode,
		TargetRef: targetRef,
	}
	var importID string
	completed := map[string]storage.GitImportedCommitRecord{}
	if req.Resume {
		record, err := s.Repository.GetOrCreateGitImport(ctx, subjectID, source, mountPath, slice.Ref, slice.Id, targetRef, mode, len(gitCommits))
		if err != nil {
			return nil, grpcError(err)
		}
		importID = record.ID
		rows, err := s.Repository.ListGitImportCommits(ctx, importID)
		if err != nil {
			return nil, grpcError(err)
		}
		for _, row := range rows {
			completed[row.GitCommitID] = row
		}
	}
	var previousGitCommitID string
	for i, gitCommitID := range gitCommits {
		if existing, ok := completed[gitCommitID]; ok {
			response.Commits = append(response.Commits, &corev1.ImportedGitCommit{
				GitCommitId:    gitCommitID,
				NativeCommitId: existing.NativeCommitID,
				Message:        existing.Message,
			})
			response.FinalCommitId = existing.NativeCommitID
			previousGitCommitID = gitCommitID
			if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
				Phase:            "skipped",
				Message:          existing.Message,
				Current:          int64(i + 1),
				Total:            int64(len(gitCommits)),
				GitCommitId:      gitCommitID,
				NativeCommitId:   existing.NativeCommitID,
				ChangedPathCount: int32(existing.ChangedPathCount),
			}); err != nil {
				return nil, err
			}
			continue
		}
		meta, err := gitCommitMetadata(ctx, repoDir, gitCommitID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "read git commit message %s: %v", gitCommitID, err)
		}
		message := meta.Subject
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:       "reading_commit",
			Message:     message,
			Current:     int64(i + 1),
			Total:       int64(len(gitCommits)),
			GitCommitId: gitCommitID,
		}); err != nil {
			return nil, err
		}
		var edits []*corev1.FileEdit
		var snapshot importSnapshot
		if previousGitCommitID == "" {
			snapshot, err = gitSnapshot(ctx, repoDir, gitCommitID, mountPath)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "read git commit %s: %v", gitCommitID, err)
			}
			edits = diffImportSnapshots(previous, snapshot)
		} else {
			edits, snapshot, err = gitDeltaEdits(ctx, repoDir, previousGitCommitID, gitCommitID, mountPath)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "read git commit %s: %v", gitCommitID, err)
			}
		}
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:            "uploading_blobs",
			Message:          message,
			Current:          int64(i + 1),
			Total:            int64(len(gitCommits)),
			GitCommitId:      gitCommitID,
			ChangedPathCount: int32(len(edits)),
		}); err != nil {
			return nil, err
		}
		if err := s.uploadImportBlobs(ctx, slice.Id, edits, snapshot); err != nil {
			return nil, grpcError(err)
		}
		ref, err := s.Repository.GetRef(ctx, targetRef)
		if err != nil {
			return nil, grpcError(err)
		}
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:            "submitting",
			Message:          message,
			Current:          int64(i + 1),
			Total:            int64(len(gitCommits)),
			GitCommitId:      gitCommitID,
			ChangedPathCount: int32(len(edits)),
		}); err != nil {
			return nil, err
		}
		patchset, err := s.createImportPatchset(ctx, subjectID, slice, targetRef, ref.CommitId, message, meta.Body, edits)
		if err != nil {
			return nil, err
		}
		if _, err := s.Changesets.Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
			return nil, grpcError(err)
		}
		nativeCommitID, err := s.waitForImportPublished(ctx, patchset.ChangesetId, len(edits))
		if err != nil {
			return nil, grpcError(err)
		}
		if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:            "published",
			Message:          message,
			Current:          int64(i + 1),
			Total:            int64(len(gitCommits)),
			GitCommitId:      gitCommitID,
			NativeCommitId:   nativeCommitID,
			ChangedPathCount: int32(len(edits)),
		}); err != nil {
			return nil, err
		}
		response.Commits = append(response.Commits, &corev1.ImportedGitCommit{
			GitCommitId:    gitCommitID,
			NativeCommitId: nativeCommitID,
			Message:        message,
		})
		response.FinalCommitId = nativeCommitID
		if importID != "" {
			if err := s.Repository.RecordGitImportCommit(ctx, storage.GitImportedCommitRecord{
				ImportID:         importID,
				GitCommitID:      gitCommitID,
				NativeCommitID:   nativeCommitID,
				Message:          message,
				Position:         i + 1,
				ChangedPathCount: len(edits),
				AuthorName:       meta.AuthorName,
				AuthorEmail:      meta.AuthorEmail,
				AuthoredAt:       meta.AuthoredAt,
				FullMessage:      meta.FullMessage,
			}); err != nil {
				return nil, grpcError(err)
			}
		}
		previousGitCommitID = gitCommitID
	}
	// Drain the derived-index outbox once, after all commits are published, so
	// imported history is queryable (gs log / slice history) when the import
	// returns — without paying the drain on every commit's critical path.
	if drainer, ok := s.Changesets.(storage.DerivedIndexStore); ok {
		if err := drainer.WaitForOutboxDrain(ctx); err != nil {
			return nil, grpcError(err)
		}
	}
	if importID != "" && response.FinalCommitId != "" {
		if err := s.Repository.CompleteGitImport(ctx, importID, response.FinalCommitId); err != nil {
			return nil, grpcError(err)
		}
	}
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "done",
		Message: "import complete",
		Current: int64(len(gitCommits)),
		Total:   int64(len(gitCommits)),
		Result:  response,
	}); err != nil {
		return nil, err
	}
	return response, nil
}

func emitImportProgress(progress func(*corev1.ImportGitRepositoryProgress) error, event *corev1.ImportGitRepositoryProgress) error {
	if progress == nil {
		return nil
	}
	return progress(event)
}

func (s *RepositoryService) currentMountedSnapshot(ctx context.Context, targetRef, mountPath string) (importSnapshot, error) {
	ref, err := s.Repository.GetRef(ctx, targetRef)
	if err != nil {
		return nil, err
	}
	files, err := s.Repository.ListFiles(ctx, ref.CommitId, mountPath)
	if err != nil {
		return nil, err
	}
	snapshot := importSnapshot{}
	for _, file := range files {
		snapshot[file.Path] = importFile{
			Path:        file.Path,
			BlobID:      file.BlobID,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
			Size:        file.Size,
		}
	}
	return snapshot, nil
}

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

func (s *RepositoryService) uploadImportBlobs(ctx context.Context, sliceID string, edits []*corev1.FileEdit, snapshot importSnapshot) error {
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

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(importBlobUploadConcurrency)
	for _, blob := range blobs {
		blob := blob
		group.Go(func() error {
			if err := s.putImportBlob(groupCtx, blob.key, blob.data); err != nil {
				return err
			}
			return s.Blobs.Upsert(groupCtx, blob.blobID, blob.contentHash, blob.size, blob.key)
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	contentHashes := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		contentHashes = append(contentHashes, blob.contentHash)
	}
	return s.Blobs.AssociateSlices(ctx, sliceID, contentHashes)
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
