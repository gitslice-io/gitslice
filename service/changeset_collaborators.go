package service

import (
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *ChangesetService) AddChangesetCollaborator(ctx context.Context, req *corev1.AddChangesetCollaboratorRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	changesetID := strings.TrimSpace(req.GetChangesetId())
	targetRef := strings.TrimSpace(req.GetUsername())
	if changesetID == "" {
		return nil, status.Error(codes.InvalidArgument, "changeset id is required")
	}
	if targetRef == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, changesetID, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	targetSubjectID, err := s.resolveSubjectIdentifier(ctx, targetRef)
	if err != nil {
		return nil, err
	}
	if err := requireTargetProposeAccess(ctx, s.Auth, targetSubjectID, targetRef, slice); err != nil {
		return nil, err
	}
	updated, err := s.Changesets.AddCollaborator(ctx, cs.Id, targetSubjectID, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	storage.PopulateChangesetHandles(updated)
	if err := s.resolveAuthors(ctx, updated); err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

func (s *ChangesetService) RemoveChangesetCollaborator(ctx context.Context, req *corev1.RemoveChangesetCollaboratorRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	changesetID := strings.TrimSpace(req.GetChangesetId())
	targetRef := strings.TrimSpace(req.GetUsername())
	if changesetID == "" {
		return nil, status.Error(codes.InvalidArgument, "changeset id is required")
	}
	if targetRef == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, changesetID, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	targetSubjectID, err := s.resolveSubjectIdentifier(ctx, targetRef)
	if err != nil {
		return nil, err
	}
	updated, err := s.Changesets.RemoveCollaborator(ctx, cs.Id, targetSubjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	storage.PopulateChangesetHandles(updated)
	if err := s.resolveAuthors(ctx, updated); err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

func (s *ChangesetService) HandoffChangeset(ctx context.Context, req *corev1.HandoffChangesetRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	changesetID := strings.TrimSpace(req.GetChangesetId())
	targetRef := strings.TrimSpace(req.GetUsername())
	if changesetID == "" {
		return nil, status.Error(codes.InvalidArgument, "changeset id is required")
	}
	if targetRef == "" {
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, changesetID, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	newAuthorID, err := s.resolveSubjectIdentifier(ctx, targetRef)
	if err != nil {
		return nil, err
	}
	if err := requireTargetProposeAccess(ctx, s.Auth, newAuthorID, targetRef, slice); err != nil {
		return nil, err
	}
	updated, err := s.Changesets.Handoff(ctx, cs.Id, newAuthorID)
	if err != nil {
		return nil, grpcError(err)
	}
	storage.PopulateChangesetHandles(updated)
	if err := s.resolveAuthors(ctx, updated); err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

func (s *ChangesetService) resolveSubjectIdentifier(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", status.Error(codes.InvalidArgument, "username is required")
	}
	if s.Auth == nil {
		return "", grpcError(storage.ErrInvalid)
	}
	for _, candidate := range []string{strings.ToLower(raw), raw} {
		subjectID, err := s.Auth.SubjectIDForUsername(ctx, candidate)
		if err == nil && strings.TrimSpace(subjectID) != "" {
			return strings.TrimSpace(subjectID), nil
		}
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return "", grpcError(err)
		}
	}
	subjects, err := s.Auth.SubjectsByIDs(ctx, []string{raw})
	if err != nil {
		return "", grpcError(err)
	}
	if subj, ok := subjects[raw]; ok && strings.TrimSpace(subj.ID) != "" {
		return strings.TrimSpace(subj.ID), nil
	}
	return "", status.Errorf(codes.NotFound, "no user or agent %s", raw)
}

func requireTargetProposeAccess(ctx context.Context, auth storage.AuthStore, targetSubjectID, displayRef string, slice *corev1.Slice) error {
	err := authz.New(auth).Authorize(ctx, targetSubjectID, slice, authz.ActionPropose)
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrUnauthorized) {
		return status.Errorf(
			codes.FailedPrecondition,
			"%s does not have propose access to slice %s/%s",
			displayRef,
			slice.GetRef().GetAccount(),
			slice.GetRef().GetSlice(),
		)
	}
	return grpcError(err)
}
