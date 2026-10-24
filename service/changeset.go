package service

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ChangesetService struct {
	Auth        storage.AuthStore
	Blobs       storage.BlobStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	Agents      storage.AgentStore
	Checks      storage.CheckStore
	ObjectStore ObjectStore
	Analytics   analytics.Client
	validator   diffValidator
	dispatcher  *checkDispatcher
}

type diffValidator struct {
	Blobs      storage.BlobStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
}

const diffFileConcurrency = 64

func (s *ChangesetService) CreateChangeset(ctx context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if err := requireDefaultTargetRef(req.TargetRef); err != nil {
		return nil, err
	}
	targetRef := req.TargetRef
	if targetRef == "" {
		targetRef = storage.DefaultTargetRef
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionWrite); err != nil {
		return nil, err
	}
	createReq := &corev1.CreateChangesetRequest{
		AuthoringSlice:    req.AuthoringSlice,
		TargetRef:         targetRef,
		BaseCommitId:      req.BaseCommitId,
		Title:             req.Title,
		Description:       req.Description,
		StackId:           req.StackId,
		ParentChangesetId: req.ParentChangesetId,
		ParentPatchsetId:  req.ParentPatchsetId,
	}
	cs, err := s.Changesets.Create(ctx, subjectID, createReq)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveAuthors(ctx, cs); err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetService) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	subjectID := optionalSubject(ctx)
	return s.getAuthorizedChangeset(ctx, subjectID, req.ChangesetId)
}

func (s *ChangesetService) ListChangesets(ctx context.Context, req *corev1.ListChangesetsRequest) (*corev1.ListChangesetsResponse, error) {
	subjectID := optionalSubject(ctx)
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionRead); err != nil {
		return nil, err
	}
	filter := storage.ChangesetListFilter{
		Slice:     req.AuthoringSlice,
		Statuses:  listStatuses(req),
		Query:     req.Query,
		Limit:     int(req.Limit),
		PageToken: req.PageToken,
		Summary:   req.Summary,
	}
	if author := strings.TrimSpace(req.Author); author != "" {
		filter.FilterAuthor = true
		// A name nobody has leaves the subject empty, which matches no one.
		if subjectID, err := s.Auth.SubjectIDForUsername(ctx, author); err == nil {
			filter.AuthorSubjectID = subjectID
		} else if !errors.Is(err, storage.ErrNotFound) {
			return nil, grpcError(err)
		}
	}
	changesets, nextPageToken, err := s.Changesets.List(ctx, filter)
	if err != nil {
		if errors.Is(err, storage.ErrInvalid) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return nil, grpcError(err)
	}
	for _, cs := range changesets {
		storage.PopulateChangesetHandles(cs)
	}
	if err := s.resolveAuthors(ctx, changesets...); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListChangesetsResponse{Changesets: changesets, NextPageToken: nextPageToken}, nil
}

// listStatuses is the statuses a list asks for: status and statuses together.
func listStatuses(req *corev1.ListChangesetsRequest) []string {
	var out []string
	for _, name := range append([]string{req.Status}, req.Statuses...) {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// resolveAuthors rewrites every Changeset.Author and Patchset.Author from the
// internal subject id to the author's username. Unresolved subjects (no personal
// account) are left unchanged so the response still carries a stable identifier.
func (s *ChangesetService) resolveAuthors(ctx context.Context, changesets ...*corev1.Changeset) error {
	seen := map[string]struct{}{}
	var subjectIDs []string
	addSubject := func(subjectID string) {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" {
			return
		}
		if _, ok := seen[subjectID]; ok {
			return
		}
		seen[subjectID] = struct{}{}
		subjectIDs = append(subjectIDs, subjectID)
	}

	for _, cs := range changesets {
		if cs == nil {
			continue
		}
		addSubject(cs.Author)
		for _, patchset := range cs.Patchsets {
			if patchset != nil {
				addSubject(patchset.Author)
			}
		}
	}
	if len(subjectIDs) == 0 {
		return nil
	}

	usernames, err := s.Auth.UsernamesForSubjects(ctx, subjectIDs)
	if err != nil {
		return err
	}
	for _, cs := range changesets {
		if cs == nil {
			continue
		}
		if username := usernames[cs.Author]; username != "" {
			cs.Author = username
		}
		for _, patchset := range cs.Patchsets {
			if patchset == nil {
				continue
			}
			if username := usernames[patchset.Author]; username != "" {
				patchset.Author = username
			}
		}
	}
	return nil
}

func (s *ChangesetService) SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForWriteOrAuthor(ctx, subjectID, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	extraCheckStatuses, err := s.skippedRequiredCheckStatuses(ctx, cs)
	if err != nil {
		return nil, grpcError(err)
	}
	emitSubmitted := cs.Status != "pending_publish" && cs.Status != "submitted"
	res, err := s.submitPatchset(ctx, cs.Id, acceptedExpectedPatchset(cs, req.ExpectedCurrentPatchsetId), extraCheckStatuses)
	if isStaleBase(err) {
		if mergedRes, mergedErr, applied := s.mergeAndSubmit(ctx, cs); applied {
			if mergedErr != nil {
				return nil, mergedErr
			}
			res, err = mergedRes, nil
		}
	}
	if err != nil {
		return nil, grpcError(err)
	}
	if emitSubmitted {
		s.captureChangesetSubmitted(ctx, cs)
	}
	return res, nil
}

// submitPatchset submits a changeset at expected, with the store's check
// statuses when it takes them.
func (s *ChangesetService) submitPatchset(ctx context.Context, changesetID, expected string, extraCheckStatuses map[string]string) (*corev1.SubmitChangesetResponse, error) {
	if submitter, ok := s.Changesets.(changesetSubmitWithCheckStatuses); ok {
		return submitter.SubmitWithCheckStatuses(ctx, changesetID, expected, extraCheckStatuses)
	}
	return s.Changesets.Submit(ctx, changesetID, expected)
}

func (s *ChangesetService) captureChangesetSubmitted(ctx context.Context, cs *corev1.Changeset) {
	if cs == nil {
		return
	}
	captureAnalytics(ctx, s.Analytics, analytics.EventChangesetSubmitted, map[string]any{
		analytics.PropChangesetID: cs.Id,
	})
}

// UpdateChangesetInfo changes a changeset's title and description while it is
// open. Its author and writers of its slice may.
func (s *ChangesetService) UpdateChangesetInfo(ctx context.Context, req *corev1.UpdateChangesetInfoRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Title == nil && req.Description == nil {
		return nil, status.Error(codes.InvalidArgument, "pass a title, a description, or both")
	}
	cs, err := s.getChangesetForWriteOrAuthor(ctx, subjectID, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	updated, err := s.Changesets.UpdateInfo(ctx, cs.Id, req.Title, req.Description)
	if err != nil {
		return nil, grpcError(err)
	}
	storage.PopulateChangesetHandles(updated)
	if err := s.resolveAuthors(ctx, updated); err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

func (s *ChangesetService) ApproveChangeset(ctx context.Context, req *corev1.ApproveChangesetRequest) (*corev1.ApproveChangesetResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	res, err := s.Changesets.Approve(ctx, cs.Id, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetService) ReportCheckResult(ctx context.Context, req *corev1.ReportCheckResultRequest) (*corev1.ReportCheckResultResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	res, err := s.Changesets.ReportCheckResult(ctx, cs.Id, subjectID, req.CheckName, req.Status)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetService) AbandonChangeset(ctx context.Context, req *corev1.AbandonChangesetRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForWriteOrAuthor(ctx, subjectID, req.ChangesetId)
	if err != nil {
		return nil, err
	}
	if err := s.Changesets.Abandon(ctx, cs.Id); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *ChangesetService) getAuthorizedChangeset(ctx context.Context, subjectID, changesetID string) (*corev1.Changeset, error) {
	cs, err := s.getChangesetForAction(ctx, subjectID, changesetID, authz.ActionRead)
	if err != nil {
		return nil, err
	}
	if err := s.resolveAuthors(ctx, cs); err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetService) getChangesetForAction(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if cs.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no authoring slice")
	}
	slice, err := resolveAuthoringSlice(ctx, s.Slices, cs.AuthoringSlice, action)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, action); err != nil {
		return nil, err
	}
	storage.PopulateChangesetHandles(cs)
	return cs, nil
}

func (s *ChangesetService) getChangesetForWriteOrAuthor(ctx context.Context, subjectID, changesetID string) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if cs.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no authoring slice")
	}
	if cs.Author == subjectID {
		storage.PopulateChangesetHandles(cs)
		return cs, nil
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionWrite); err != nil {
		return nil, err
	}
	storage.PopulateChangesetHandles(cs)
	return cs, nil
}
