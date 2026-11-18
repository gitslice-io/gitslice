package service

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ChangesetService struct {
	*changesetCore
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
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	if stackID := strings.TrimSpace(req.StackId); stackID != "" {
		// Adding to a stack takes the same ownership as AddStackEntry.
		stacks := s.stacks()
		stack, err := stacks.getStackWithSlice(ctx, stackID, authz.ActionPropose)
		if err != nil {
			return nil, err
		}
		if err := stacks.authorizeAddEntry(ctx, subjectID, stack, req.ParentChangesetId); err != nil {
			return nil, err
		}
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
	storeCtx, _ := withCapturedAttribution(ctx, s.Auth, subjectID)
	cs, err := s.Changesets.Create(storeCtx, subjectID, createReq)
	if err != nil {
		return nil, grpcError(err)
	}
	s.linkMatchingIntents(ctx, slice.GetId(), subjectID, cs.GetId(), cs.GetTitle(), cs.GetDescription(), nil)
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

// resolveAuthors rewrites every Changeset.Author, OriginalAuthor, Collaborators,
// and Patchset.Author from internal subject ids to usernames. Unresolved subjects
// (no personal account) are left unchanged so the response still carries a stable identifier.
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
		addSubject(cs.OriginalAuthor)
		for _, collab := range cs.Collaborators {
			addSubject(collab)
		}
		for _, patchset := range cs.Patchsets {
			if patchset != nil {
				addSubject(patchset.Author)
			}
		}
		for _, app := range cs.Approvals {
			if app != nil {
				addSubject(app.SubjectId)
			}
		}
	}
	if len(subjectIDs) > 0 && s.Auth != nil {
		usernames, err := newSubjectDirectory(s.Auth).usernames(ctx, subjectIDs...)
		if err != nil {
			return err
		}
		for _, cs := range changesets {
			if cs == nil {
				continue
			}
			if username := usernames[strings.TrimSpace(cs.Author)]; username != "" {
				cs.Author = username
			}
			if username := usernames[strings.TrimSpace(cs.OriginalAuthor)]; username != "" {
				cs.OriginalAuthor = username
			}
			for i, collab := range cs.Collaborators {
				if username := usernames[strings.TrimSpace(collab)]; username != "" {
					cs.Collaborators[i] = username
				}
			}
			for _, patchset := range cs.Patchsets {
				if patchset == nil {
					continue
				}
				if username := usernames[strings.TrimSpace(patchset.Author)]; username != "" {
					patchset.Author = username
				}
			}
			for _, app := range cs.Approvals {
				if app == nil {
					continue
				}
				if username := usernames[strings.TrimSpace(app.SubjectId)]; username != "" {
					app.Username = username
				}
			}
		}
	}
	return enrichActorAttributions(ctx, s.Auth, s.Agents, collectChangesetAttributions(changesets...)...)
}

func (s *ChangesetService) SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionSubmit)
	if err != nil {
		return nil, err
	}
	extraCheckStatuses, err := s.submitExtras(ctx, cs)
	if err != nil {
		return nil, grpcError(err)
	}
	ctx, _ = withCapturedAttribution(ctx, s.Auth, subjectID)
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
	if res != nil && res.Attribution != nil {
		if err := enrichActorAttributions(ctx, s.Auth, s.Agents, res.Attribution); err != nil {
			return nil, grpcError(err)
		}
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
// open. Only an owner of the changeset (or a slice admin) may.
func (s *ChangesetService) UpdateChangesetInfo(ctx context.Context, req *corev1.UpdateChangesetInfoRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Title == nil && req.Description == nil {
		return nil, status.Error(codes.InvalidArgument, "pass a title, a description, or both")
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	updated, err := s.Changesets.UpdateInfo(ctx, cs.Id, req.Title, req.Description)
	if err != nil {
		return nil, grpcError(err)
	}
	if slice, sliceErr := s.Slices.Resolve(ctx, updated.GetAuthoringSlice()); sliceErr == nil && slice != nil {
		s.linkMatchingIntents(ctx, slice.GetId(), updated.GetAuthor(), updated.GetId(), updated.GetTitle(), updated.GetDescription(), updated.GetAffectedPaths())
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
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionReview)
	if err != nil {
		return nil, err
	}
	storeCtx, _ := withCapturedAttribution(ctx, s.Auth, subjectID)
	res, err := s.Changesets.Approve(storeCtx, cs.Id, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	if res != nil && res.Attribution != nil {
		if err := enrichActorAttributions(ctx, s.Auth, s.Agents, res.Attribution); err != nil {
			return nil, grpcError(err)
		}
	}
	return res, nil
}

func (s *ChangesetService) ReportCheckResult(ctx context.Context, req *corev1.ReportCheckResultRequest) (*corev1.ReportCheckResultResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetWithSliceAction(ctx, subjectID, req.ChangesetId, authz.ActionSubmit)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, cs.GetAuthoringSlice())
	if err != nil {
		return nil, grpcError(err)
	}
	checkName := strings.TrimSpace(req.GetCheckName())
	if isRequiredSliceCheck(slice, checkName) {
		if normalized, ok := storage.NormalizeCheckStatus(req.GetStatus()); ok && normalized == storage.CheckStatusPass {
			patchset := currentPatchset(cs)
			isController, _ := isControllerOfSubject(ctx, s.Auth, subjectID, cs.GetAuthor())
			isAuthor := subjectID == strings.TrimSpace(cs.GetAuthor()) ||
				isController ||
				hasCollaboratorSubject(cs, subjectID) ||
				(patchset != nil && subjectID == strings.TrimSpace(patchset.GetAuthor()))
			if (isAuthor && len(storage.CIDaemonPool(slice)) > 0) || s.isRequiredCheckOwnedByCIOrTrigger(ctx, cs, slice, patchset, checkName) {
				return nil, status.Errorf(codes.PermissionDenied, "required check %q is gated by CI or triggers and cannot be marked passing manually", checkName)
			}
		}
	}
	res, err := s.Changesets.ReportCheckResult(ctx, cs.Id, subjectID, req.CheckName, req.Status)
	if err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetService) isRequiredCheckOwnedByCIOrTrigger(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, checkName string) bool {
	checkName = strings.TrimSpace(checkName)
	if checkName == "" {
		return false
	}
	if s.Checks != nil && cs != nil && patchset != nil {
		if runs, err := s.Checks.ListCheckRuns(ctx, cs.GetId(), patchset.GetId()); err == nil {
			for _, run := range runs {
				if strings.TrimSpace(run.GetCheckName()) == checkName && (run.GetProvenance() == "ci" || run.GetProvenance() == "trigger") {
					return true
				}
			}
		}
	}
	if s.dispatcher != nil && slice != nil && patchset != nil {
		if plan, err := s.dispatcher.resolveCheckPlan(ctx, slice, patchset); err == nil {
			if spec, ok := plan.Spec(checkName); ok {
				if len(storage.CIDaemonPool(slice)) > 0 || spec.OutOfSlice {
					return true
				}
			}
			if len(storage.CIDaemonPool(slice)) > 0 && checkPlanDefines(plan, checkName) {
				return true
			}
		}
	}
	if s.Repository != nil && s.ObjectStore != nil && cs != nil && patchset != nil {
		account := cs.GetAuthoringSlice().GetAccount()
		ref := cs.GetTargetRef()
		if ref == "" {
			ref = triggers.DefaultRef
		}
		files := TreeFiles{Repository: s.Repository, Objects: s.ObjectStore}
		changes := triggers.ChangesFromPatchset(patchset)
		if head, err := s.Repository.GetRef(ctx, ref); err == nil {
			if tree, err := s.Repository.RootTreeForCommit(ctx, head.GetCommitId()); err == nil {
				if _, declared, err := triggers.CheckSkipped(ctx, files, tree, account, ref, checkName, changes); err == nil && declared {
					return true
				}
			}
		}
		if patchset.GetResultTreeId() != "" {
			if _, declared, err := triggers.CheckSkipped(ctx, files, patchset.GetResultTreeId(), account, ref, checkName, changes); err == nil && declared {
				return true
			}
		}
	}
	return false
}

func checkPlanDefines(plan *checks.Plan, checkName string) bool {
	if plan == nil {
		return false
	}
	if _, ok := plan.Spec(checkName); ok {
		return true
	}
	for _, skipped := range plan.Skipped {
		if skipped.Name == checkName {
			return true
		}
	}
	for _, errored := range plan.Errored {
		if errored.Name == checkName {
			return true
		}
	}
	return false
}

func (s *ChangesetService) AbandonChangeset(ctx context.Context, req *corev1.AbandonChangesetRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.getChangesetForAction(ctx, subjectID, req.ChangesetId, authz.ActionPropose)
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

// getChangesetWithSliceAction checks slice-level capability without requiring
// changeset ownership (used by ReportCheckResult).
func (s *ChangesetService) getChangesetWithSliceAction(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, error) {
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
