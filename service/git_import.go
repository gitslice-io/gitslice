package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/gitimport"
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
	sink := gitimport.NewProgress(progress)
	if progress != nil {
		progress = sink.Emit
	}
	defer sink.Heartbeat()()

	flightKey := strings.Join([]string{subjectID, source, mountPath, targetRef, mode, strconv.Itoa(int(req.MaxCommits))}, "\x00")
	releaseFlight, err := s.importFlights.Acquire(ctx, flightKey, func() {
		_ = gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
			Phase:   "waiting",
			Message: "waiting for this import's previous connection to finish its commit",
		})
	})
	if err != nil {
		return nil, err
	}
	defer releaseFlight()

	if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "cloning",
		Message: "cloning git repository",
	}); err != nil {
		return nil, err
	}

	var repoDir string
	var cleanup func()
	if mode == "deep" && req.MaxCommits == 0 && !localSource {
		repoDir, cleanup, err = s.importCloneCache().Acquire(ctx, source, false)
	} else {
		repoDir, cleanup, err = gitimport.Clone(ctx, source, mode, int(req.MaxCommits), localSource)
	}
	if err != nil {
		return nil, gitimport.CloneError(ctx, err)
	}
	defer cleanup()

	if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
		Phase:   "listing_commits",
		Message: "listing git commits",
	}); err != nil {
		return nil, err
	}
	gitCommits, err := gitimport.SelectedCommits(ctx, repoDir, mode, int(req.MaxCommits))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "list git commits: %v", err)
	}
	if len(gitCommits) == 0 {
		return nil, status.Error(codes.InvalidArgument, "git repository has no commits")
	}
	if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
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
	known := gitimport.KnownBlobs{}
	for i, gitCommitID := range gitCommits {
		if existing, ok := completed[gitCommitID]; ok {
			response.Commits = append(response.Commits, &corev1.ImportedGitCommit{
				GitCommitId:    gitCommitID,
				NativeCommitId: existing.NativeCommitID,
				Message:        existing.Message,
			})
			response.FinalCommitId = existing.NativeCommitID
			previousGitCommitID = gitCommitID
			if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
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
		work := func(ctx context.Context) error {
			meta, err := gitimport.ReadCommitMetadata(ctx, repoDir, gitCommitID)
			if err != nil {
				return gitimport.ReadError(err, "read git commit message "+gitCommitID)
			}
			message := meta.Subject
			if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
				Phase:       "reading_commit",
				Message:     message,
				Current:     int64(i + 1),
				Total:       int64(len(gitCommits)),
				GitCommitId: gitCommitID,
			}); err != nil {
				return err
			}
			var deleted, upsertGitPaths []string
			var snapshot gitimport.Snapshot
			var blobIDs []string
			if previousGitCommitID == "" {
				snapshot, blobIDs, err = gitimport.SnapshotMetadata(ctx, repoDir, gitCommitID, mountPath)
				if err != nil {
					return gitimport.ReadError(err, "read git commit "+gitCommitID)
				}
			} else {
				deleted, upsertGitPaths, snapshot, blobIDs, err = gitimport.DeltaMetadata(ctx, repoDir, previousGitCommitID, gitCommitID, mountPath)
				if err != nil {
					return gitimport.ReadError(err, "read git commit "+gitCommitID)
				}
			}
			changedPaths := int32(len(deleted) + len(snapshot))
			if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
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
				_ = gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
					Phase:            "uploading_blobs",
					Message:          fmt.Sprintf("%s (%d of %d new files stored)", message, done, total),
					Current:          int64(i + 1),
					Total:            int64(len(gitCommits)),
					GitCommitId:      gitCommitID,
					ChangedPathCount: changedPaths,
				})
			}
			if err := gitimport.StreamAndUploadBlobs(ctx, s.Blobs, s.ObjectStore, repoDir, slice.Id, snapshot, blobIDs, known, uploadProgress); err != nil {
				var uploadErr *gitimport.UploadError
				if errors.As(err, &uploadErr) {
					return grpcError(uploadErr.Err)
				}
				return gitimport.ReadError(err, "read git commit "+gitCommitID)
			}
			var edits []*corev1.FileEdit
			if previousGitCommitID == "" {
				edits = gitimport.DiffSnapshots(previous, snapshot)
			} else {
				edits, err = gitimport.DeltaEditsFromSnapshot(mountPath, deleted, upsertGitPaths, snapshot)
				if err != nil {
					return gitimport.ReadError(err, "read git commit "+gitCommitID)
				}
			}
			ref, err := s.Repository.GetRef(ctx, targetRef)
			if err != nil {
				return grpcError(err)
			}
			if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
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
			nativeCommitID, err := gitimport.WaitForPublished(ctx, s.Changesets, patchset.ChangesetId, len(edits))
			if err != nil {
				return grpcError(err)
			}
			if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
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
		workCtx, cancelWork := gitimport.WorkContext(ctx)
		err := work(workCtx)
		cancelWork()
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		previousGitCommitID = gitCommitID
	}

	finalCtx, cancelFinal := gitimport.WorkContext(ctx)
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
	if err := gitimport.EmitProgress(progress, &corev1.ImportGitRepositoryProgress{
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

func (s *RepositoryService) currentMountedSnapshot(ctx context.Context, targetRef, mountPath string) (gitimport.Snapshot, error) {
	ref, err := s.Repository.GetRef(ctx, targetRef)
	if err != nil {
		return nil, err
	}
	files, err := s.Repository.ListFiles(ctx, ref.CommitId, mountPath)
	if err != nil {
		return nil, err
	}
	snapshot := gitimport.Snapshot{}
	for _, file := range files {
		snapshot[file.Path] = gitimport.File{
			Path:        file.Path,
			BlobID:      file.BlobID,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
			Size:        file.Size,
		}
	}
	return snapshot, nil
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
	label := firstNonEmptyString(storage.ShortChangesetID(cs.GetId()), cs.GetId())
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK && st.Code() != codes.Unknown {
		return status.Errorf(st.Code(), "import stopped: %s (changeset %s is left open for approval)", st.Message(), label)
	}
	return status.Errorf(codes.FailedPrecondition, "import stopped: %v (changeset %s is left open for approval)", err, label)
}
