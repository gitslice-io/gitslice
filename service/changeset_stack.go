package service

import (
	"context"
	"errors"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type ChangesetStackService struct {
	Auth        storage.AuthStore
	Blobs       storage.BlobStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	ObjectStore ObjectStore
	validator   diffValidator
}

func (s *ChangesetStackService) CreateStack(ctx context.Context, req *corev1.CreateStackRequest) (*corev1.ChangesetStack, error) {
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
	stack, err := s.Changesets.CreateStack(ctx, subjectID, &corev1.CreateStackRequest{
		AuthoringSlice: req.AuthoringSlice,
		TargetRef:      targetRef,
		BaseCommitId:   req.BaseCommitId,
		Title:          req.Title,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveStackAuthors(ctx, stack); err != nil {
		return nil, grpcError(err)
	}
	return stack, nil
}

func (s *ChangesetStackService) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := s.getAuthorizedStack(ctx, subjectID, req.StackId, authz.ActionRead)
	if err != nil {
		return nil, err
	}
	if err := s.resolveStackAuthors(ctx, stack); err != nil {
		return nil, grpcError(err)
	}
	return stack, nil
}

func (s *ChangesetStackService) ListStacks(ctx context.Context, req *corev1.ListStacksRequest) (*corev1.ListStacksResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.AuthoringSlice == nil {
		return nil, status.Error(codes.InvalidArgument, "authoring slice is required")
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionRead); err != nil {
		return nil, err
	}
	stacks, err := s.Changesets.ListStacks(ctx, req)
	if err != nil {
		return nil, grpcError(err)
	}
	for _, stack := range stacks {
		if err := s.resolveStackAuthors(ctx, stack); err != nil {
			return nil, grpcError(err)
		}
	}
	return &corev1.ListStacksResponse{Stacks: stacks}, nil
}

func (s *ChangesetStackService) AddStackEntry(ctx context.Context, req *corev1.AddStackEntryRequest) (*corev1.Changeset, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := s.getAuthorizedStack(ctx, subjectID, req.StackId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	cs, err := s.Changesets.Create(ctx, subjectID, &corev1.CreateChangesetRequest{
		AuthoringSlice:    stack.AuthoringSlice,
		TargetRef:         stack.TargetRef,
		BaseCommitId:      stack.BaseCommitId,
		Title:             req.Title,
		Description:       req.Description,
		StackId:           stack.Id,
		ParentChangesetId: req.ParentChangesetId,
		ParentPatchsetId:  req.ParentPatchsetId,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveChangesetAuthors(ctx, cs); err != nil {
		return nil, grpcError(err)
	}
	return cs, nil
}

func (s *ChangesetStackService) MoveStackEntry(ctx context.Context, req *corev1.MoveStackEntryRequest) (*corev1.ChangesetStack, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.getAuthorizedStack(ctx, subjectID, req.StackId, authz.ActionWrite); err != nil {
		return nil, err
	}
	stack, err := s.Changesets.MoveStackEntry(ctx, req)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveStackAuthors(ctx, stack); err != nil {
		return nil, grpcError(err)
	}
	return stack, nil
}

func (s *ChangesetStackService) ReparentStackEntry(ctx context.Context, req *corev1.ReparentStackEntryRequest) (*corev1.ChangesetStack, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.getAuthorizedStack(ctx, subjectID, req.StackId, authz.ActionWrite); err != nil {
		return nil, err
	}
	stack, err := s.Changesets.ReparentStackEntry(ctx, req)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveStackAuthors(ctx, stack); err != nil {
		return nil, grpcError(err)
	}
	return stack, nil
}

func (s *ChangesetStackService) DetachStackEntry(ctx context.Context, req *corev1.DetachStackEntryRequest) (*corev1.DetachStackEntryResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.getAuthorizedStack(ctx, subjectID, req.StackId, authz.ActionWrite); err != nil {
		return nil, err
	}
	res, err := s.Changesets.DetachStackEntry(ctx, subjectID, req)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveStackAuthors(ctx, res.SourceStack); err != nil {
		return nil, grpcError(err)
	}
	if err := s.resolveStackAuthors(ctx, res.DetachedStack); err != nil {
		return nil, grpcError(err)
	}
	return res, nil
}

func (s *ChangesetStackService) Restack(ctx context.Context, req *corev1.RestackRequest) (*corev1.RestackResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := s.getAuthorizedStack(ctx, subjectID, req.StackId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	entries := selectedRestackEntries(stack, req.StartChangesetId, req.IncludeSiblings)
	if len(entries) == 0 {
		return nil, status.Error(codes.NotFound, "stack entry not found")
	}
	changesetService := ChangesetService{
		Auth:        s.Auth,
		Blobs:       s.Blobs,
		Changesets:  s.Changesets,
		Repository:  s.Repository,
		Slices:      s.Slices,
		ObjectStore: s.ObjectStore,
		validator:   s.validator,
	}
	response := &corev1.RestackResponse{StackId: stack.Id, Status: "clean"}
	for _, entry := range entries {
		cs, err := s.Changesets.Get(ctx, entry.ChangesetId)
		if err != nil {
			return nil, grpcError(err)
		}
		if cs.Status == "submitted" {
			continue
		}
		current := currentPatchset(cs)
		if current == nil {
			continue
		}
		if entry.ParentChangesetId == "" {
			if strings.TrimSpace(req.TargetBaseCommitId) == "" {
				continue
			}
			refreshed, conflicted, err := s.restackUpdateOrConflict(ctx, changesetService, cs, &corev1.UpdateChangesetRequest{
				ChangesetId:               cs.Id,
				ExpectedCurrentPatchsetId: cs.CurrentPatchsetId,
				BaseCommitId:              req.TargetBaseCommitId,
				BaseKind:                  "commit",
				FileEdits:                 cloneFileEdits(current.FileEdits),
				PatchsetKind:              current.Kind,
			})
			if err != nil {
				return nil, err
			}
			if conflicted {
				response.Status = "conflicts"
			}
			response.Entries = append(response.Entries, refreshed)
			continue
		}
		parent, err := s.Changesets.Get(ctx, entry.ParentChangesetId)
		if err != nil {
			return nil, grpcError(err)
		}
		parentPatchset := currentPatchset(parent)
		if parentPatchset == nil || parentPatchset.ResultTreeId == "" {
			return nil, status.Error(codes.FailedPrecondition, "parent changeset has no preview patchset")
		}
		if current.BasePatchsetId == parentPatchset.Id && entry.State != "needs_restack" {
			continue
		}
		refreshed, conflicted, err := s.restackUpdateOrConflict(ctx, changesetService, cs, &corev1.UpdateChangesetRequest{
			ChangesetId:               cs.Id,
			ExpectedCurrentPatchsetId: cs.CurrentPatchsetId,
			BaseCommitId:              cs.BaseCommitId,
			BaseKind:                  "patchset",
			BasePatchsetId:            parentPatchset.Id,
			ExpectedParentPatchsetId:  parentPatchset.Id,
			FileEdits:                 cloneFileEdits(current.FileEdits),
			PatchsetKind:              current.Kind,
		})
		if err != nil {
			return nil, err
		}
		if conflicted {
			response.Status = "conflicts"
		}
		response.Entries = append(response.Entries, refreshed)
	}
	if err := s.resolveChangesetAuthors(ctx, response.Entries...); err != nil {
		return nil, grpcError(err)
	}
	if stack.Status == "partial" {
		if err := s.Changesets.SetStackStatus(ctx, stack.Id, "open"); err != nil {
			return nil, grpcError(err)
		}
	}
	return response, nil
}

func (s *ChangesetStackService) restackUpdateOrConflict(ctx context.Context, changesetService ChangesetService, cs *corev1.Changeset, req *corev1.UpdateChangesetRequest) (*corev1.Changeset, bool, error) {
	attemptedEdits := cloneFileEdits(req.FileEdits)
	if _, err := changesetService.UpdateChangeset(ctx, req); err != nil {
		if !restackReplayMayBecomeConflict(err) {
			return nil, false, err
		}
		conflictReq := proto.Clone(req).(*corev1.UpdateChangesetRequest)
		conflictReq.FileEdits = nil
		conflictReq.Conflicts = restackConflictsFromEdits(cs, attemptedEdits, req.BaseCommitId)
		conflictReq.PatchsetKind = "conflict"
		if len(conflictReq.Conflicts) == 0 {
			conflictReq.Conflicts = []*corev1.PatchsetConflict{{
				Path:            "/",
				ConflictClass:   "restack",
				OldBaseCommitId: cs.BaseCommitId,
				NewBaseCommitId: req.BaseCommitId,
			}}
		}
		if _, conflictErr := changesetService.UpdateChangeset(ctx, conflictReq); conflictErr != nil {
			return nil, false, err
		}
		refreshed, getErr := s.Changesets.Get(ctx, cs.Id)
		if getErr != nil {
			return nil, false, grpcError(getErr)
		}
		return refreshed, true, nil
	}
	refreshed, err := s.Changesets.Get(ctx, cs.Id)
	if err != nil {
		return nil, false, grpcError(err)
	}
	return refreshed, false, nil
}

func restackReplayMayBecomeConflict(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.FailedPrecondition, codes.NotFound:
		return true
	default:
		return false
	}
}

func restackConflictsFromEdits(cs *corev1.Changeset, edits []*corev1.FileEdit, newBaseCommitID string) []*corev1.PatchsetConflict {
	seen := map[string]*corev1.PatchsetConflict{}
	out := []*corev1.PatchsetConflict{}
	add := func(p string, edit *corev1.FileEdit) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if existing, ok := seen[p]; ok {
			if existing.LocalContentHash == "" && edit != nil && edit.Path == p && edit.ContentHash != "" && edit.Op != "delete" {
				existing.LocalContentHash = edit.ContentHash
			}
			return
		}
		conflict := &corev1.PatchsetConflict{
			Path:            p,
			ConflictClass:   "restack",
			OldBaseCommitId: cs.BaseCommitId,
			NewBaseCommitId: newBaseCommitID,
		}
		if edit != nil && edit.Path == p && edit.ContentHash != "" && edit.Op != "delete" {
			conflict.LocalContentHash = edit.ContentHash
		}
		seen[p] = conflict
		out = append(out, conflict)
	}
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		add(edit.Path, edit)
		add(edit.OldPath, edit)
	}
	return out
}

func (s *ChangesetStackService) SubmitStack(ctx context.Context, req *corev1.SubmitStackRequest) (*corev1.SubmitStackResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := s.getAuthorizedStack(ctx, subjectID, req.StackId, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	entries := selectedStackEntries(stack, req.SubtreeRootChangesetId)
	if len(entries) == 0 {
		return nil, status.Error(codes.NotFound, "stack entry not found")
	}
	response := &corev1.SubmitStackResponse{StackId: stack.Id, Status: "submitted"}
	blocked := false
	for _, entry := range entries {
		cs := entry.Changeset
		if cs == nil {
			var err error
			cs, err = s.Changesets.Get(ctx, entry.ChangesetId)
			if err != nil {
				return nil, grpcError(err)
			}
		}
		if entry.State == "needs_restack" {
			blocked = true
			response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
				ChangesetId:   entry.ChangesetId,
				Status:        "blocked",
				BlockedReason: "NeedsBaseUpdate",
			})
			continue
		}
		if cs.Status == "submitted" {
			response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
				ChangesetId: entry.ChangesetId,
				Status:      "submitted",
				CommitId:    cs.CommitId,
			})
			continue
		}
		res, err := s.Changesets.Submit(ctx, entry.ChangesetId, cs.CurrentPatchsetId)
		if err != nil {
			blocked = true
			reason := submitBlockReason(err)
			if refreshed, getErr := s.Changesets.Get(ctx, entry.ChangesetId); getErr == nil && refreshed.SubmitBlockedReason != "" {
				reason = refreshed.SubmitBlockedReason
			}
			response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
				ChangesetId:   entry.ChangesetId,
				Status:        "blocked",
				BlockedReason: reason,
			})
			continue
		}
		response.Results = append(response.Results, &corev1.SubmitStackEntryResult{
			ChangesetId:      entry.ChangesetId,
			Status:           res.Status,
			CommitId:         res.CommitId,
			PendingPublishId: res.PendingPublishId,
		})
	}
	if blocked {
		response.Status = "partial"
		if err := s.Changesets.SetStackStatus(ctx, stack.Id, "partial"); err != nil {
			return nil, grpcError(err)
		}
		return response, nil
	}
	if stack.Status == "partial" {
		if err := s.Changesets.SetStackStatus(ctx, stack.Id, "open"); err != nil {
			return nil, grpcError(err)
		}
	}
	return response, nil
}

func (s *ChangesetStackService) getAuthorizedStack(ctx context.Context, subjectID, stackID string, action authz.Action) (*corev1.ChangesetStack, error) {
	stack, err := s.Changesets.GetStack(ctx, strings.TrimSpace(stackID))
	if err != nil {
		return nil, grpcError(err)
	}
	if stack.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "stack has no authoring slice")
	}
	slice, err := s.Slices.Resolve(ctx, stack.AuthoringSlice)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, action); err != nil {
		return nil, err
	}
	return stack, nil
}

func (s *ChangesetStackService) resolveStackAuthors(ctx context.Context, stack *corev1.ChangesetStack) error {
	if stack == nil {
		return nil
	}
	changesets := make([]*corev1.Changeset, 0, len(stack.Entries))
	for _, entry := range stack.Entries {
		if entry != nil && entry.Changeset != nil {
			changesets = append(changesets, entry.Changeset)
		}
	}
	return s.resolveChangesetAuthors(ctx, changesets...)
}

func (s *ChangesetStackService) resolveChangesetAuthors(ctx context.Context, changesets ...*corev1.Changeset) error {
	changesetService := ChangesetService{Auth: s.Auth}
	return changesetService.resolveAuthors(ctx, changesets...)
}

func selectedStackEntries(stack *corev1.ChangesetStack, subtreeRoot string) []*corev1.ChangesetStackEntry {
	if stack == nil {
		return nil
	}
	subtreeRoot = strings.TrimSpace(subtreeRoot)
	if subtreeRoot == "" {
		return stack.Entries
	}
	descendants := map[string]struct{}{subtreeRoot: {}}
	changed := true
	for changed {
		changed = false
		for _, entry := range stack.Entries {
			if entry == nil {
				continue
			}
			if _, ok := descendants[entry.ChangesetId]; ok {
				continue
			}
			if _, ok := descendants[entry.ParentChangesetId]; ok {
				descendants[entry.ChangesetId] = struct{}{}
				changed = true
			}
		}
	}
	out := make([]*corev1.ChangesetStackEntry, 0, len(descendants))
	for _, entry := range stack.Entries {
		if _, ok := descendants[entry.ChangesetId]; ok {
			out = append(out, entry)
		}
	}
	return out
}

func selectedRestackEntries(stack *corev1.ChangesetStack, startChangesetID string, includeSiblings bool) []*corev1.ChangesetStackEntry {
	startChangesetID = strings.TrimSpace(startChangesetID)
	if startChangesetID == "" {
		return selectedStackEntries(stack, "")
	}
	if !includeSiblings {
		return selectedStackEntries(stack, startChangesetID)
	}
	start := stackEntryByID(stack, startChangesetID)
	if start == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, entry := range stack.Entries {
		if entry == nil || entry.ParentChangesetId != start.ParentChangesetId {
			continue
		}
		for _, selected := range selectedStackEntries(stack, entry.ChangesetId) {
			seen[selected.ChangesetId] = struct{}{}
		}
	}
	out := make([]*corev1.ChangesetStackEntry, 0, len(seen))
	for _, entry := range stack.Entries {
		if _, ok := seen[entry.ChangesetId]; ok {
			out = append(out, entry)
		}
	}
	return out
}

func stackEntryByID(stack *corev1.ChangesetStack, changesetID string) *corev1.ChangesetStackEntry {
	if stack == nil {
		return nil
	}
	for _, entry := range stack.Entries {
		if entry != nil && entry.ChangesetId == changesetID {
			return entry
		}
	}
	return nil
}

func cloneFileEdits(in []*corev1.FileEdit) []*corev1.FileEdit {
	out := make([]*corev1.FileEdit, 0, len(in))
	for _, edit := range in {
		if edit == nil {
			continue
		}
		out = append(out, proto.Clone(edit).(*corev1.FileEdit))
	}
	return out
}

func submitBlockReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, storage.ErrConflict) {
		return strings.TrimSpace(strings.TrimPrefix(err.Error(), storage.ErrConflict.Error()+":"))
	}
	return err.Error()
}
