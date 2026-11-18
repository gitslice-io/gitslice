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

// authorizeChangesetForAction enforces both slice capability and changeset
// ownership (design/36 §2) in one place:
//   - ActionRead and ActionReview apply slice-wide (plus controller(cs.Author)
//     delegation when cs.Author holds the capability on the slice).
//   - All other actions (ActionPropose, ActionSubmit, etc.) require the caller
//     to own cs — cs.Author, controller(cs.Author) acting with the higher of
//     their own role and cs.Author's role, cs.Collaborators within their own
//     slice role, or a slice/account admin (ActionActOnAnyChangeset).
func (s *ChangesetService) authorizeChangesetForAction(ctx context.Context, subjectID string, cs *corev1.Changeset, action authz.Action) (*corev1.Changeset, error) {
	if cs.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no authoring slice")
	}
	slice, err := resolveAuthoringSlice(ctx, s.Slices, cs.AuthoringSlice, action)
	if err != nil {
		return nil, grpcError(err)
	}

	subjectID = strings.TrimSpace(subjectID)
	authorID := strings.TrimSpace(cs.GetAuthor())

	if action == authz.ActionRead || action == authz.ActionReview {
		err := authorize(ctx, s.Auth, subjectID, slice, action)
		if err != nil && status.Code(err) == codes.PermissionDenied && subjectID != "" {
			if isCtrl, ctrlErr := isControllerOfSubject(ctx, s.Auth, subjectID, authorID); ctrlErr != nil {
				return nil, grpcError(ctrlErr)
			} else if isCtrl {
				err = authorizeWithDelegation(ctx, s.Auth, subjectID, authorID, slice, action)
			}
		}
		if err != nil {
			return nil, err
		}
		storage.PopulateChangesetHandles(cs)
		return cs, nil
	}

	if subjectID == "" {
		return nil, grpcError(storage.ErrUnauthenticated)
	}

	// 1. Direct author.
	if subjectID == authorID {
		if err := authorize(ctx, s.Auth, subjectID, slice, action); err != nil {
			return nil, err
		}
		storage.PopulateChangesetHandles(cs)
		return cs, nil
	}

	// 2. Controller of cs.Author (acts with the higher of controller's role and cs.Author's role).
	isCtrl, err := isControllerOfSubject(ctx, s.Auth, subjectID, authorID)
	if err != nil {
		return nil, grpcError(err)
	}
	if isCtrl {
		if err := authorizeWithDelegation(ctx, s.Auth, subjectID, authorID, slice, action); err != nil {
			return nil, err
		}
		storage.PopulateChangesetHandles(cs)
		return cs, nil
	}

	// 3. Explicit collaborator (acts within their own slice role).
	if hasCollaboratorSubject(cs, subjectID) {
		if err := authorize(ctx, s.Auth, subjectID, slice, action); err != nil {
			return nil, err
		}
		storage.PopulateChangesetHandles(cs)
		return cs, nil
	}

	// 4. Slice or account admin (ActionActOnAnyChangeset). Check admin role
	// ignoring archived first so an admin on an archived slice gets the
	// archived FailedPrecondition error from authorize rather than PermissionDenied.
	if authz.New(s.Auth).AuthorizeAllowArchived(ctx, subjectID, slice, authz.ActionActOnAnyChangeset) == nil {
		if err := authorize(ctx, s.Auth, subjectID, slice, action); err != nil {
			return nil, err
		}
		storage.PopulateChangesetHandles(cs)
		return cs, nil
	}

	return nil, status.Errorf(codes.PermissionDenied, "only its author, the person an agent author works for, a collaborator or a slice admin may change, submit or abandon changeset %s; ask the author to add you (gs cs collaborators add %s <you>) or hand it over (gs cs handoff %s <you>)", changesetLabel(cs), changesetLabel(cs), changesetLabel(cs))
}

func (s *ChangesetService) getChangesetForAction(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	return s.authorizeChangesetForAction(ctx, subjectID, cs, action)
}

func hasCollaboratorSubject(cs *corev1.Changeset, subjectID string) bool {
	if cs == nil || subjectID == "" {
		return false
	}
	for _, collab := range cs.GetCollaborators() {
		if strings.TrimSpace(collab) == subjectID {
			return true
		}
	}
	return false
}

func isControllerOfSubject(ctx context.Context, auth storage.AuthStore, actorSubjectID, targetSubjectID string) (bool, error) {
	actorSubjectID = strings.TrimSpace(actorSubjectID)
	targetSubjectID = strings.TrimSpace(targetSubjectID)
	if actorSubjectID == "" || targetSubjectID == "" || actorSubjectID == targetSubjectID || auth == nil {
		return false, nil
	}
	subjects, err := auth.SubjectsByIDs(ctx, []string{targetSubjectID})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(subjects[targetSubjectID].ControllerSubjectID) == actorSubjectID, nil
}

func authorizeWithDelegation(ctx context.Context, auth storage.AuthStore, actorSubjectID, delegatedSubjectID string, slice *corev1.Slice, action authz.Action) error {
	if err := authz.New(auth).AuthorizeDelegated(ctx, actorSubjectID, delegatedSubjectID, slice, action); err != nil {
		return grpcError(err)
	}
	return nil
}

func changesetLabel(cs *corev1.Changeset) string {
	if short := storage.ShortChangesetID(cs.GetId()); short != "" {
		return short
	}
	return cs.GetId()
}
