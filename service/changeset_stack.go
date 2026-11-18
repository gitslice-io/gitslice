package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ChangesetStackService struct {
	*changesetCore
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
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.AuthoringSlice, authz.ActionPropose); err != nil {
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
	stack, err := s.getStackWithSlice(ctx, req.StackId, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeAddEntry(ctx, subjectID, stack, req.ParentChangesetId); err != nil {
		return nil, err
	}
	storeCtx, _ := withCapturedAttribution(ctx, s.Auth, subjectID)
	cs, err := s.Changesets.Create(storeCtx, subjectID, &corev1.CreateChangesetRequest{
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
	if s.Slices != nil {
		if slice, sliceErr := s.Slices.Resolve(ctx, stack.GetAuthoringSlice()); sliceErr == nil && slice != nil {
			linkMatchingIntentsForStore(ctx, s.Changesets, slice.GetId(), subjectID, cs.GetId(), cs.GetTitle(), cs.GetDescription(), nil)
		}
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
	stack, err := s.getStackWithSlice(ctx, req.StackId, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	if entry := stackEntryByID(stack, strings.TrimSpace(req.ChangesetId)); entry != nil {
		if err := s.authorizeStackEntryChangeset(ctx, subjectID, entry, authz.ActionPropose); err != nil {
			return nil, err
		}
	} else if err := s.authorizeStackOwner(ctx, subjectID, stack, authz.ActionPropose); err != nil {
		return nil, err
	}
	stack, err = s.Changesets.MoveStackEntry(ctx, req)
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
	stack, err := s.getStackWithSlice(ctx, req.StackId, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	if entry := stackEntryByID(stack, strings.TrimSpace(req.ChangesetId)); entry != nil {
		if err := s.authorizeStackEntries(ctx, subjectID, selectedStackEntries(stack, entry.ChangesetId), authz.ActionPropose, false); err != nil {
			return nil, err
		}
	} else if err := s.authorizeStackOwner(ctx, subjectID, stack, authz.ActionPropose); err != nil {
		return nil, err
	}
	stack, err = s.Changesets.ReparentStackEntry(ctx, req)
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
	stack, err := s.getStackWithSlice(ctx, req.StackId, authz.ActionPropose)
	if err != nil {
		return nil, err
	}
	targetID := strings.TrimSpace(req.ChangesetId)
	if targetID != "" && stackEntryByID(stack, targetID) != nil {
		entries := selectedStackEntries(stack, targetID)
		if err := s.authorizeStackEntries(ctx, subjectID, entries, authz.ActionPropose, false); err != nil {
			return nil, err
		}
	} else if err := s.authorizeStackOwner(ctx, subjectID, stack, authz.ActionPropose); err != nil {
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

// authorizeAddEntry checks who may add a changeset to a stack: an owner of the
// parent entry (or of the stack), as AddStackEntry and CreateChangeset with a
// stack_id both require.
func (s *ChangesetStackService) authorizeAddEntry(ctx context.Context, subjectID string, stack *corev1.ChangesetStack, parentChangesetID string) error {
	parentID := strings.TrimSpace(parentChangesetID)
	if parentID != "" {
		if parentEntry := stackEntryByID(stack, parentID); parentEntry != nil {
			err := s.authorizeStackEntryChangeset(ctx, subjectID, parentEntry, authz.ActionPropose)
			if err == nil {
				return nil
			}
			if status.Code(err) != codes.PermissionDenied {
				return err
			}
			if stackErr := s.authorizeStackCreatorOrAdmin(ctx, subjectID, stack, authz.ActionPropose); stackErr != nil {
				return err
			}
			return nil
		}
	}
	return s.authorizeStackOwner(ctx, subjectID, stack, authz.ActionPropose)
}

func (s *ChangesetStackService) getStackWithSlice(ctx context.Context, stackID string, action authz.Action) (*corev1.ChangesetStack, error) {
	stack, err := s.Changesets.GetStack(ctx, strings.TrimSpace(stackID))
	if err != nil {
		return nil, grpcError(err)
	}
	if stack.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "stack has no authoring slice")
	}
	if _, err := resolveAuthoringSlice(ctx, s.Slices, stack.AuthoringSlice, action); err != nil {
		return nil, grpcError(err)
	}
	return stack, nil
}

func (s *ChangesetStackService) authorizeStackEntryChangeset(ctx context.Context, subjectID string, entry *corev1.ChangesetStackEntry, action authz.Action) error {
	if entry == nil {
		return status.Error(codes.NotFound, "stack entry not found")
	}
	cs := entry.Changeset
	if cs == nil {
		var err error
		cs, err = s.Changesets.Get(ctx, entry.ChangesetId)
		if err != nil {
			return grpcError(err)
		}
	}
	changesets := s.changesetService()
	_, err := changesets.authorizeChangesetForAction(ctx, subjectID, cs, action)
	return err
}

func (s *ChangesetStackService) authorizeStackEntries(ctx context.Context, subjectID string, entries []*corev1.ChangesetStackEntry, action authz.Action, skipSubmittedWhenOpenRemain bool) error {
	var openEntries []*corev1.ChangesetStackEntry
	if skipSubmittedWhenOpenRemain {
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			cs := entry.Changeset
			if cs == nil {
				var err error
				cs, err = s.Changesets.Get(ctx, entry.ChangesetId)
				if err != nil {
					return grpcError(err)
				}
			}
			if cs.Status != "submitted" {
				openEntries = append(openEntries, &corev1.ChangesetStackEntry{
					StackId:     entry.StackId,
					ChangesetId: entry.ChangesetId,
					Changeset:   cs,
				})
			}
		}
	}
	toCheck := entries
	if skipSubmittedWhenOpenRemain && len(openEntries) > 0 {
		toCheck = openEntries
	}
	for _, entry := range toCheck {
		if err := s.authorizeStackEntryChangeset(ctx, subjectID, entry, action); err != nil {
			return err
		}
	}
	return nil
}

func (s *ChangesetStackService) authorizeStackCreatorOrAdmin(ctx context.Context, subjectID string, stack *corev1.ChangesetStack, action authz.Action) error {
	slice, err := resolveAuthoringSlice(ctx, s.Slices, stack.AuthoringSlice, action)
	if err != nil {
		return grpcError(err)
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return grpcError(storage.ErrUnauthenticated)
	}
	createdBy := strings.TrimSpace(stack.GetCreatedBy())
	if subjectID == createdBy {
		return authorize(ctx, s.Auth, subjectID, slice, action)
	}
	isCtrl, err := isControllerOfSubject(ctx, s.Auth, subjectID, createdBy)
	if err != nil {
		return grpcError(err)
	}
	if isCtrl {
		return authorizeWithDelegation(ctx, s.Auth, subjectID, createdBy, slice, action)
	}
	if authz.New(s.Auth).AuthorizeAllowArchived(ctx, subjectID, slice, authz.ActionActOnAnyChangeset) == nil {
		return authorize(ctx, s.Auth, subjectID, slice, action)
	}
	return status.Error(codes.PermissionDenied, "only its creator, the person an agent creator works for, an owner of one of its changesets or a slice admin may change this stack")
}

func (s *ChangesetStackService) authorizeStackOwner(ctx context.Context, subjectID string, stack *corev1.ChangesetStack, action authz.Action) error {
	err := s.authorizeStackCreatorOrAdmin(ctx, subjectID, stack, action)
	if err == nil || status.Code(err) != codes.PermissionDenied {
		return err
	}
	for _, entry := range stack.Entries {
		if entry == nil {
			continue
		}
		if entryErr := s.authorizeStackEntryChangeset(ctx, subjectID, entry, action); entryErr == nil {
			return nil
		} else if status.Code(entryErr) != codes.PermissionDenied {
			return entryErr
		}
	}
	return err
}

func (s *ChangesetStackService) getAuthorizedStack(ctx context.Context, subjectID, stackID string, action authz.Action) (*corev1.ChangesetStack, error) {
	stack, err := s.Changesets.GetStack(ctx, strings.TrimSpace(stackID))
	if err != nil {
		return nil, grpcError(err)
	}
	if stack.AuthoringSlice == nil {
		return nil, status.Error(codes.FailedPrecondition, "stack has no authoring slice")
	}
	slice, err := resolveAuthoringSlice(ctx, s.Slices, stack.AuthoringSlice, action)
	if err != nil {
		return nil, grpcError(err)
	}
	err = authorize(ctx, s.Auth, subjectID, slice, action)
	if err != nil && action == authz.ActionRead && status.Code(err) == codes.PermissionDenied && strings.TrimSpace(subjectID) != "" {
		if isCtrl, ctrlErr := isControllerOfSubject(ctx, s.Auth, subjectID, stack.GetCreatedBy()); ctrlErr != nil {
			return nil, grpcError(ctrlErr)
		} else if isCtrl {
			err = authorizeWithDelegation(ctx, s.Auth, subjectID, stack.GetCreatedBy(), slice, authz.ActionRead)
		}
		if err != nil && status.Code(err) == codes.PermissionDenied {
			for _, entry := range stack.Entries {
				if entry != nil && s.authorizeStackEntryChangeset(ctx, subjectID, entry, authz.ActionRead) == nil {
					err = nil
					break
				}
			}
		}
	}
	if err != nil {
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
	cs := s.changesetService()
	return cs.resolveAuthors(ctx, changesets...)
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

// changesetService is a ChangesetService over the shared changesetCore, for
// the changeset operations a stack operation makes (new patchsets, submits).
func (s *ChangesetStackService) changesetService() ChangesetService {
	return s.changesets()
}
