package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pathOperationPublishTimeout bounds the wait for a copy or move to publish.
// The publish is one tree edit plus path heads under the destination, so it is
// quick, but it shares the target ref's publish lock with other changesets.
const pathOperationPublishTimeout = 2 * time.Minute

type changesetSubmitBypassingRequirements interface {
	SubmitBypassingRequirements(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error)
}

// CopyPath copies a file or folder and lands it at once. The source may be in
// another slice, even another account's, when the caller can read it there.
func (s *ChangesetService) CopyPath(ctx context.Context, req *corev1.CopyPathRequest) (*corev1.PathOperationResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, source, destination, err := s.pathOperationTarget(ctx, subjectID, req.GetSlice(), req.GetSourcePath(), req.GetDestinationPath())
	if err != nil {
		return nil, err
	}
	var readable []string
	if !paths.InAnyPrefix(slice.GetDefinition().GetIncludedPaths(), source) {
		if req.GetSourceSlice() == nil {
			return nil, status.Errorf(codes.InvalidArgument, "%s is outside slice %s/%s; name the slice it is read through (source_slice)", source, slice.GetRef().GetAccount(), slice.GetRef().GetSlice())
		}
		sourceSlice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.GetSourceSlice(), authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if !paths.InAnyPrefix(sourceSlice.GetDefinition().GetIncludedPaths(), source) {
			return nil, status.Errorf(codes.FailedPrecondition, "%s is outside slice %s/%s", source, sourceSlice.GetRef().GetAccount(), sourceSlice.GetRef().GetSlice())
		}
		readable = []string{source}
	}
	edit := &corev1.FileEdit{Op: "copy", OldPath: source, Path: destination}
	return s.landPathOperation(ctx, subjectID, slice, edit, readable, fmt.Sprintf("Copy %s to %s", source, destination))
}

// MovePath moves a file or folder within its slice and lands it at once.
func (s *ChangesetService) MovePath(ctx context.Context, req *corev1.MovePathRequest) (*corev1.PathOperationResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, source, destination, err := s.pathOperationTarget(ctx, subjectID, req.GetSlice(), req.GetSourcePath(), req.GetDestinationPath())
	if err != nil {
		return nil, err
	}
	included := slice.GetDefinition().GetIncludedPaths()
	if !paths.InAnyPrefix(included, source) {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is outside slice %s/%s; a move can't leave its slice", source, slice.GetRef().GetAccount(), slice.GetRef().GetSlice())
	}
	if isIncludedPath(included, source) {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is one of the slice's included paths; change the slice definition to move it", source)
	}
	if paths.Contains(source, destination) {
		return nil, status.Errorf(codes.InvalidArgument, "can't move %s into itself", source)
	}
	edit := &corev1.FileEdit{Op: "rename", OldPath: source, Path: destination}
	return s.landPathOperation(ctx, subjectID, slice, edit, nil, fmt.Sprintf("Move %s to %s", source, destination))
}

// pathOperationTarget authorizes the caller to write slice and checks the two
// paths: canonical, different, and a destination inside the slice below its
// included paths.
func (s *ChangesetService) pathOperationTarget(ctx context.Context, subjectID string, ref *corev1.SliceRef, sourcePath, destinationPath string) (*corev1.Slice, string, string, error) {
	if ref == nil {
		return nil, "", "", status.Error(codes.InvalidArgument, "slice is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionWrite)
	if err != nil {
		return nil, "", "", err
	}
	source, err := paths.Canonical(sourcePath)
	if err != nil {
		return nil, "", "", status.Errorf(codes.InvalidArgument, "source path: %v", err)
	}
	destination, err := paths.Canonical(destinationPath)
	if err != nil {
		return nil, "", "", status.Errorf(codes.InvalidArgument, "destination path: %v", err)
	}
	if source == destination {
		return nil, "", "", status.Error(codes.InvalidArgument, "source and destination must differ")
	}
	included := slice.GetDefinition().GetIncludedPaths()
	if !paths.InAnyPrefix(included, destination) {
		return nil, "", "", status.Errorf(codes.FailedPrecondition, "%s is outside slice %s/%s", destination, slice.GetRef().GetAccount(), slice.GetRef().GetSlice())
	}
	if isIncludedPath(included, destination) {
		return nil, "", "", status.Errorf(codes.FailedPrecondition, "%s is one of the slice's included paths; choose a path inside it", destination)
	}
	return slice, source, destination, nil
}

// landPathOperation makes a changeset of the single edit on the latest commit
// and submits it without the slice's required approvals and checks, then
// waits for the commit. The edit relocates entries already in the tree and
// never carries new content, which is why it may skip review.
func (s *ChangesetService) landPathOperation(ctx context.Context, subjectID string, slice *corev1.Slice, edit *corev1.FileEdit, readable []string, title string) (*corev1.PathOperationResponse, error) {
	submitter, ok := s.Changesets.(changesetSubmitBypassingRequirements)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "this server can't land a copy or move directly")
	}
	ref, err := s.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		return nil, grpcError(err)
	}
	baseCommitID := ref.GetCommitId()
	if _, err := s.Repository.GetEntry(ctx, baseCommitID, edit.OldPath); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "%s does not exist", edit.OldPath)
		}
		return nil, grpcError(err)
	}
	if _, err := s.Repository.GetEntry(ctx, baseCommitID, edit.Path); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "%s already exists", edit.Path)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, grpcError(err)
	}
	edits := []*corev1.FileEdit{edit}
	validation, err := s.validator.validateFileEditsReading(ctx, slice, baseCommitID, "", edits, true, readable)
	if err != nil {
		return nil, err
	}
	cs, err := s.Changesets.Create(ctx, subjectID, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.Ref,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   baseCommitID,
		Title:          title,
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
		s.abandonPathOperation(ctx, cs.Id)
		return nil, grpcError(err)
	}
	if _, err := submitter.SubmitBypassingRequirements(ctx, cs.Id, patchset.Id); err != nil {
		s.abandonPathOperation(ctx, cs.Id)
		if errors.Is(err, storage.ErrConflict) {
			return nil, status.Errorf(codes.Aborted, "%v; try again", err)
		}
		return nil, grpcError(err)
	}
	s.captureChangesetSubmitted(ctx, cs)
	commitID, err := s.waitForPathOperationPublished(ctx, cs.Id)
	if err != nil {
		return nil, err
	}
	landed, err := s.Changesets.Get(ctx, cs.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	storage.PopulateChangesetHandles(landed)
	if err := s.resolveAuthors(ctx, landed); err != nil {
		return nil, grpcError(err)
	}
	captureAnalytics(ctx, s.Analytics, analytics.EventPatchsetPushed, map[string]any{
		analytics.PropChangesetID: cs.Id,
		analytics.PropPatchsetID:  patchset.Id,
	})
	return &corev1.PathOperationResponse{Changeset: landed, CommitId: commitID}, nil
}

// abandonPathOperation drops the changeset of a copy or move that could not
// land, so it does not linger as a draft. Its error is ignored: at worst a
// draft is left that the author can abandon.
func (s *ChangesetService) abandonPathOperation(ctx context.Context, changesetID string) {
	_ = s.Changesets.Abandon(context.WithoutCancel(ctx), changesetID)
}

func (s *ChangesetService) waitForPathOperationPublished(ctx context.Context, changesetID string) (string, error) {
	deadline := time.Now().Add(pathOperationPublishTimeout)
	for {
		cs, err := s.Changesets.Get(ctx, changesetID)
		if err != nil {
			return "", grpcError(err)
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs.CommitId, nil
		}
		if _, err := s.Changesets.PublishPending(ctx, 16); err != nil && !errors.Is(err, storage.ErrConflict) {
			return "", grpcError(err)
		}
		if time.Now().After(deadline) {
			return "", status.Errorf(codes.DeadlineExceeded, "changeset %s was submitted but has not published yet; it will land shortly", changesetID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// isIncludedPath reports whether p is exactly one of the slice's included
// paths: the slice definition owns those folders, not its edits.
func isIncludedPath(included []string, p string) bool {
	for _, prefix := range included {
		if canonical, err := paths.CanonicalPrefix(prefix); err == nil && canonical == p {
			return true
		}
	}
	return false
}
