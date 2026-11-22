package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
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
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionSubmit)
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
	// A client that drops does not fail progress events; the import checks
	// for it between commits. A heartbeat tells the client it is still going.
	sink := newImportProgress(progress)
	if progress != nil {
		progress = sink.emit
	}
	defer sink.heartbeat()()
	// One import of this source into this place at a time: a retry that
	// arrives while the previous attempt is still finishing its commit waits
	// for it, then resumes after it.
	flightKey := strings.Join([]string{subjectID, source, mountPath, targetRef, mode, strconv.Itoa(int(req.MaxCommits))}, "\x00")
	releaseFlight, err := s.importFlights.acquire(ctx, flightKey, func() {
		_ = emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:   "waiting",
			Message: "waiting for this import's previous connection to finish its commit",
		})
	})
	if err != nil {
		return nil, err
	}
	defer releaseFlight()
	if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "cloning",
		Message: "cloning git repository",
	}); err != nil {
		return nil, err
	}
	// A full deep import of a remote repository keeps its clone between
	// attempts: a client on a flaky link resumes from the kept clone instead
	// of cloning the whole history again each time.
	var repoDir string
	var cleanup func()
	if mode == "deep" && req.MaxCommits == 0 && !localSource {
		repoDir, cleanup, err = s.importCloneCache().acquire(ctx, source, false)
	} else {
		repoDir, cleanup, err = cloneForImport(ctx, source, mode, int(req.MaxCommits), localSource)
	}
	if err != nil {
		return nil, cloneError(ctx, err)
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
	known := knownBlobs{}
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
		// The commit's work runs apart from the client's request: a client
		// whose connection drops while it is published does not leave it half
		// done, and its retry finds it recorded and skips it.
		work := func(ctx context.Context) error {
			meta, err := gitCommitMetadata(ctx, repoDir, gitCommitID)
			if err != nil {
				return importReadError(err, "read git commit message "+gitCommitID)
			}
			message := meta.Subject
			if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
				Phase:       "reading_commit",
				Message:     message,
				Current:     int64(i + 1),
				Total:       int64(len(gitCommits)),
				GitCommitId: gitCommitID,
			}); err != nil {
				return err
			}
			var deleted, upsertGitPaths []string
			var snapshot importSnapshot
			var blobIDs []string
			if previousGitCommitID == "" {
				snapshot, blobIDs, err = gitSnapshotMetadata(ctx, repoDir, gitCommitID, mountPath)
				if err != nil {
					return importReadError(err, "read git commit "+gitCommitID)
				}
			} else {
				deleted, upsertGitPaths, snapshot, blobIDs, err = gitDeltaMetadata(ctx, repoDir, previousGitCommitID, gitCommitID, mountPath)
				if err != nil {
					return importReadError(err, "read git commit "+gitCommitID)
				}
			}
			changedPaths := int32(len(deleted) + len(snapshot))
			if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
				Phase:            "uploading_blobs",
				Message:          message,
				Current:          int64(i + 1),
				Total:            int64(len(gitCommits)),
				GitCommitId:      gitCommitID,
				ChangedPathCount: changedPaths,
			}); err != nil {
				return err
			}
			uploadProgress := func(done, total int) {
				_ = emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
					Phase:            "uploading_blobs",
					Message:          fmt.Sprintf("%s (%d of %d new files stored)", message, done, total),
					Current:          int64(i + 1),
					Total:            int64(len(gitCommits)),
					GitCommitId:      gitCommitID,
					ChangedPathCount: changedPaths,
				})
			}
			if err := s.streamAndUploadImportBlobs(ctx, repoDir, slice.Id, snapshot, blobIDs, known, uploadProgress); err != nil {
				var uploadErr *importUploadError
				if errors.As(err, &uploadErr) {
					return grpcError(uploadErr.err)
				}
				return importReadError(err, "read git commit "+gitCommitID)
			}
			var edits []*corev1.FileEdit
			if previousGitCommitID == "" {
				edits = diffImportSnapshots(previous, snapshot)
			} else {
				edits, err = deltaEditsFromSnapshot(mountPath, deleted, upsertGitPaths, snapshot)
				if err != nil {
					return importReadError(err, "read git commit "+gitCommitID)
				}
			}
			ref, err := s.Repository.GetRef(ctx, targetRef)
			if err != nil {
				return grpcError(err)
			}
			if err := emitImportProgress(progress, &corev1.ImportGitRepositoryProgress{
				Phase:            "submitting",
				Message:          message,
				Current:          int64(i + 1),
				Total:            int64(len(gitCommits)),
				GitCommitId:      gitCommitID,
				ChangedPathCount: int32(len(edits)),
			}); err != nil {
				return err
			}
			patchset, err := s.createImportPatchset(ctx, subjectID, slice, targetRef, ref.CommitId, message, meta.Body, edits)
			if err != nil {
				return err
			}
			if err := s.requireImportSubmitApprovals(ctx, patchset.ChangesetId, ref.CommitId); err != nil {
				return err
			}
			if _, err := s.Changesets.Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
				return grpcError(err)
			}
			nativeCommitID, err := s.waitForImportPublished(ctx, patchset.ChangesetId, len(edits))
			if err != nil {
				return grpcError(err)
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
				return err
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
					return grpcError(err)
				}
			}
			return nil
		}
		workCtx, cancelWork := importWorkContext(ctx)
		err := work(workCtx)
		cancelWork()
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			// The client went away; its commit is published. Stop here.
			return nil, status.FromContextError(err).Err()
		}
		previousGitCommitID = gitCommitID
	}
	// Drain the derived-index outbox once, after all commits are published, so
	// imported history is queryable (gs log / slice history) when the import
	// returns — without paying the drain on every commit's critical path.
	finalCtx, cancelFinal := importWorkContext(ctx)
	defer cancelFinal()
	if drainer, ok := s.Changesets.(storage.DerivedIndexStore); ok {
		if err := drainer.WaitForOutboxDrain(finalCtx); err != nil {
			return nil, grpcError(err)
		}
	}
	if importID != "" && response.FinalCommitId != "" {
		if err := s.Repository.CompleteGitImport(finalCtx, importID, response.FinalCommitId); err != nil {
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
