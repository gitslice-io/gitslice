package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxImportBatchCommits = 1000
	maxImportBatchEdits   = 250000
	maxImportSourceLength = 1024
)

func (s *RepositoryService) BeginGitImport(ctx context.Context, req *corev1.BeginGitImportRequest) (*corev1.BeginGitImportResponse, error) {
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
	if err := requireDefaultTargetRef(req.TargetRef); err != nil {
		return nil, err
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		return nil, status.Error(codes.InvalidArgument, "import source is required")
	}
	if len(source) > maxImportSourceLength {
		return nil, status.Errorf(codes.InvalidArgument, "import source exceeds %d characters", maxImportSourceLength)
	}

	res := &corev1.BeginGitImportResponse{
		TargetRef: targetRef,
		MountPath: mountPath,
	}
	if req.Resume {
		record, err := s.Repository.GetOrCreateGitImport(ctx, subjectID, source, mountPath, slice.Ref, slice.Id, targetRef, mode, int(req.TotalCommits))
		if err != nil {
			return nil, grpcError(err)
		}
		res.ImportId = record.ID
		rows, err := s.Repository.ListGitImportCommits(ctx, record.ID)
		if err != nil {
			return nil, grpcError(err)
		}
		res.CompletedCommits = make([]*corev1.ImportedGitCommit, 0, len(rows))
		for _, row := range rows {
			res.CompletedCommits = append(res.CompletedCommits, &corev1.ImportedGitCommit{
				GitCommitId:    row.GitCommitID,
				NativeCommitId: row.NativeCommitID,
				Message:        row.Message,
			})
			res.FinalCommitId = row.NativeCommitID
		}
	}

	ref, err := s.Repository.GetRef(ctx, targetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	files, err := s.Repository.ListFiles(ctx, ref.CommitId, mountPath)
	if err != nil {
		return nil, grpcError(err)
	}
	res.MountedFiles = make([]*corev1.TreeEntry, 0, len(files))
	for _, file := range files {
		res.MountedFiles = append(res.MountedFiles, &corev1.TreeEntry{
			Path:        file.Path,
			Kind:        corev1.EntryKind_ENTRY_KIND_FILE,
			BlobId:      file.BlobID,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
			Size:        file.Size,
		})
	}
	return res, nil
}

func (s *RepositoryService) ImportGitCommitBatch(ctx context.Context, req *corev1.ImportGitCommitBatchRequest) (*corev1.ImportGitCommitBatchResponse, error) {
	s.lastBulkImportUnixNano.Store(time.Now().UnixNano())
	defer func() {
		s.lastBulkImportUnixNano.Store(time.Now().UnixNano())
	}()
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
	if slice.Archived {
		return nil, status.Error(codes.FailedPrecondition, "slice is archived")
	}
	if slice.Definition != nil {
		if slice.Definition.RequiredApprovals > 0 || slice.Definition.RequiredHumanApprovals > 0 || len(slice.Definition.RequiredChecks) > 0 {
			return nil, status.Error(codes.FailedPrecondition, "slice requires approvals or checks; direct batched import cannot bypass slice submit requirements")
		}
	}
	mountPath, err := paths.Canonical(req.MountPath)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !paths.InAnyPrefix(slice.Definition.IncludedPaths, mountPath) {
		return nil, status.Errorf(codes.FailedPrecondition, "mount path %s is outside slice %s/%s", mountPath, slice.Ref.Account, slice.Ref.Slice)
	}
	if err := requireDefaultTargetRef(req.TargetRef); err != nil {
		return nil, err
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	if len(req.Commits) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one commit is required")
	}
	if len(req.Commits) > maxImportBatchCommits {
		return nil, status.Errorf(codes.InvalidArgument, "batch has %d commits, exceeds %d commit limit", len(req.Commits), maxImportBatchCommits)
	}

	// Idempotent retry: skip commits already recorded for this import session.
	existingByGitID := map[string]storage.GitImportedCommitRecord{}
	if req.ImportId != "" {
		var rows []storage.GitImportedCommitRecord
		var err error
		if targeted, ok := s.Repository.(interface {
			ListGitImportCommitsByGitIDs(context.Context, string, []string) ([]storage.GitImportedCommitRecord, error)
		}); ok {
			gitIDs := make([]string, 0, len(req.Commits))
			for _, commit := range req.Commits {
				if commit != nil && strings.TrimSpace(commit.GitCommitId) != "" {
					gitIDs = append(gitIDs, commit.GitCommitId)
				}
			}
			rows, err = targeted.ListGitImportCommitsByGitIDs(ctx, req.ImportId, gitIDs)
		} else {
			rows, err = s.Repository.ListGitImportCommits(ctx, req.ImportId)
		}
		if err != nil {
			return nil, grpcError(err)
		}
		for _, row := range rows {
			existingByGitID[row.GitCommitID] = row
		}
	}

	var totalEdits int
	var allNormalizedEdits []*corev1.FileEdit
	allChangedSet := map[string]struct{}{}
	pendingInputs := make([]*corev1.ImportGitCommitInput, 0, len(req.Commits))
	alreadyDone := make([]*corev1.ImportedGitCommit, 0)
	for _, commit := range req.Commits {
		if commit == nil || strings.TrimSpace(commit.GitCommitId) == "" {
			return nil, status.Error(codes.InvalidArgument, "each commit requires git_commit_id")
		}
		if existing, ok := existingByGitID[commit.GitCommitId]; ok {
			alreadyDone = append(alreadyDone, &corev1.ImportedGitCommit{
				GitCommitId:    existing.GitCommitID,
				NativeCommitId: existing.NativeCommitID,
				Message:        existing.Message,
			})
			continue
		}
		totalEdits += len(commit.Edits)
		if totalEdits > maxImportBatchEdits {
			return nil, status.Errorf(codes.InvalidArgument, "batch exceeds %d total edits", maxImportBatchEdits)
		}
		pendingInputs = append(pendingInputs, commit)
	}
	if len(pendingInputs) == 0 {
		res := &corev1.ImportGitCommitBatchResponse{Commits: alreadyDone}
		if len(alreadyDone) > 0 {
			res.FinalCommitId = alreadyDone[len(alreadyDone)-1].NativeCommitId
		}
		return res, nil
	}

	specs := make([]storage.GitImportCommitSpec, 0, len(pendingInputs))
	for _, commit := range pendingInputs {
		normalized := make([]*corev1.FileEdit, len(commit.Edits))
		changedSet := make(map[string]struct{}, len(commit.Edits))
		for i, edit := range commit.Edits {
			n, err := normalizeEdit(edit, true)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
			for _, p := range editPaths(n) {
				if !paths.InAnyPrefix([]string{mountPath}, p) {
					return nil, status.Errorf(codes.FailedPrecondition, "path %s is outside mount path %s", p, mountPath)
				}
				if !paths.InAnyPrefix(slice.Definition.IncludedPaths, p) {
					return nil, status.Errorf(codes.FailedPrecondition, "path %s is outside slice %s/%s", p, slice.Ref.Account, slice.Ref.Slice)
				}
				changedSet[p] = struct{}{}
				allChangedSet[p] = struct{}{}
			}
			normalized[i] = n
			allNormalizedEdits = append(allNormalizedEdits, n)
		}
		changedPaths := make([]string, 0, len(changedSet))
		for p := range changedSet {
			changedPaths = append(changedPaths, p)
		}
		sort.Strings(changedPaths)
		title := strings.TrimSpace(commit.Message)
		if title == "" {
			title = "Import git commit " + shortCommit(commit.GitCommitId)
		}
		specs = append(specs, storage.GitImportCommitSpec{
			GitCommitID:  commit.GitCommitId,
			Position:     int(commit.Position),
			Title:        title,
			Description:  commit.Description,
			FullMessage:  commit.FullMessage,
			AuthorName:   commit.AuthorName,
			AuthorEmail:  commit.AuthorEmail,
			AuthoredAt:   commit.AuthoredAt,
			ChangedPaths: changedPaths,
			FileEdits:    normalized,
		})
	}

	if err := s.validator.hydrateBlobsMetadata(ctx, allNormalizedEdits); err != nil {
		return nil, err
	}

	// Enforce .gitslice/ admin approval and .gitslice/owners.yaml path-owner
	// approval across every path touched by this batch, matching direct submit.
	allChangedPaths := make([]string, 0, len(allChangedSet))
	for p := range allChangedSet {
		allChangedPaths = append(allChangedPaths, p)
	}
	sort.Strings(allChangedPaths)
	if len(allChangedPaths) > 0 {
		ref, err := s.Repository.GetRef(ctx, targetRef)
		if err != nil {
			return nil, grpcError(err)
		}
		if err := s.requireImportBatchApprovals(ctx, subjectID, slice, ref.CommitId, allChangedPaths, allNormalizedEdits); err != nil {
			return nil, err
		}
	}

	published, err := s.Changesets.PublishGitImportBatch(ctx, storage.GitImportBatchInput{
		ImportID:  req.ImportId,
		SubjectID: subjectID,
		SliceID:   slice.Id,
		TargetRef: targetRef,
		Commits:   specs,
	})
	if err != nil {
		return nil, grpcError(err)
	}

	res := &corev1.ImportGitCommitBatchResponse{
		Commits: make([]*corev1.ImportedGitCommit, 0, len(alreadyDone)+len(published)),
	}
	res.Commits = append(res.Commits, alreadyDone...)
	for _, p := range published {
		res.Commits = append(res.Commits, &corev1.ImportedGitCommit{
			GitCommitId:    p.GitCommitID,
			NativeCommitId: p.NativeCommitID,
			Message:        p.Message,
		})
		res.FinalCommitId = p.NativeCommitID
	}
	return res, nil
}

func (s *RepositoryService) requireImportBatchApprovals(
	ctx context.Context,
	subjectID string,
	slice *corev1.Slice,
	headCommitID string,
	changedPaths []string,
	edits []*corev1.FileEdit,
) error {
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
	ps := &corev1.Patchset{
		Author:       subjectID,
		BaseCommitId: headCommitID,
		ChangedPaths: changedPaths,
		FileEdits:    edits,
	}
	if touched, err := rules.patchsetTouchedGitslicePathForSubmit(ctx, ps, headCommitID); err != nil {
		return grpcError(err)
	} else if touched != "" {
		ownerSubjects, err := rules.accountAdminSubjects(ctx, slice.Ref.Account)
		if err != nil {
			return grpcError(err)
		}
		res := rules.evaluateOwnerRule(ctx, []string{subjectID}, nil, ownerRuleSpec{
			ownerSubjects: ownerSubjects,
			require:       1,
			selfBySubject: true,
		})
		if res.err != nil {
			return res.err
		}
		if res.count < 1 {
			return status.Errorf(codes.FailedPrecondition, ".gitslice configuration path %s requires an owner or admin of %s", touched, slice.Ref.Account)
		}
	}
	return rules.requirePathOwnersForDirectOperation(ctx, headCommitID, subjectID, changedPaths)
}

func (s *RepositoryService) CompleteGitImport(ctx context.Context, req *corev1.CompleteGitImportRequest) (*corev1.CompleteGitImportResponse, error) {
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
	if err := requireDefaultTargetRef(req.TargetRef); err != nil {
		return nil, err
	}
	if drainer, ok := s.Changesets.(storage.DerivedIndexStore); ok {
		if err := drainer.WaitForOutboxDrain(ctx); err != nil {
			return nil, grpcError(err)
		}
	}
	if req.ImportId != "" && req.FinalCommitId != "" {
		if err := s.Repository.CompleteGitImport(ctx, req.ImportId, req.FinalCommitId); err != nil {
			return nil, grpcError(err)
		}
	}
	return &corev1.CompleteGitImportResponse{FinalCommitId: req.FinalCommitId}, nil
}
