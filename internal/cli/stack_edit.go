package cli

import (
	"context"
	"fmt"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) runStackMove(ctx context.Context, opts commandOptions, entry, onto string, order int64) error {
	if err := r.refuseInLazyWorkspace("gs move"); err != nil {
		return err
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return err
	}
	if state.ActiveStackID == "" {
		return userError("no_active_stack", "no active dependency tree in workspace", "Run gs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	stackClient := corev1.NewChangesetStackServiceClient(conn)
	changesetClient := corev1.NewChangesetServiceClient(conn)
	stack, err := stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return err
	}
	moved := stackEntryByChangesetSelector(stack, entry)
	if moved == nil {
		return userError("stack_entry_not_found", "changeset not found", "Run gs deps to inspect available changesets.")
	}
	if state.CurrentChangesetID != "" {
		currentSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, state.CurrentChangesetID)
		if err != nil {
			return err
		}
		edits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, currentSnapshot, false)
		if err != nil {
			return err
		}
		if changed := changedPathsFromEdits(edits); len(changed) > 0 {
			return userError("workspace_dirty", "workspace has local changes not captured in the active entry", "Run gs modify before moving entries.")
		}
	}
	parentID := ""
	parentPatchsetID := ""
	if strings.TrimSpace(onto) != "" && strings.TrimSpace(onto) != "root" {
		parent, err := changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: onto})
		if err != nil {
			return err
		}
		parentID = parent.Id
		parentPatchsetID = parent.CurrentPatchsetId
	}
	updated, err := stackClient.ReparentStackEntry(callCtx, &corev1.ReparentStackEntryRequest{
		StackId:              state.ActiveStackID,
		ChangesetId:          moved.ChangesetId,
		NewParentChangesetId: parentID,
		NewParentPatchsetId:  parentPatchsetID,
		SiblingOrder:         order,
	})
	if err != nil {
		return err
	}
	restacked, err := stackClient.Restack(callCtx, &corev1.RestackRequest{
		StackId:          state.ActiveStackID,
		StartChangesetId: moved.ChangesetId,
	})
	if err != nil {
		return err
	}
	restackedIDs := make([]string, 0, len(restacked.Entries))
	for _, cs := range restacked.Entries {
		if cs == nil {
			continue
		}
		restackedIDs = append(restackedIDs, cs.Id)
		if cs.Id == moved.ChangesetId {
			state.CurrentPatchsetID = cs.CurrentPatchsetId
		}
	}
	updated, err = stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return err
	}
	state.CurrentChangesetID = moved.ChangesetId
	if finalEntry := stackEntryByChangesetSelector(updated, moved.ChangesetId); finalEntry != nil && finalEntry.Changeset != nil {
		state.CurrentPatchsetID = finalEntry.Changeset.CurrentPatchsetId
	}
	targetSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, updated, moved.ChangesetId)
	if err != nil {
		return err
	}
	if err := r.materializeWorkspaceSnapshot(ctx, conn, cfg, ws, targetSnapshot); err != nil {
		return err
	}
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
		out := stackOutputFromProto(updated)
		out.RestackedChangesets = restackedIDs
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	printStack(r.Stdout, updated)
	if len(restackedIDs) > 0 {
		fmt.Fprintf(r.Stdout, "updated: %s\n", strings.Join(restackedIDs, ", "))
	}
	return nil
}

func (r Runner) runStackDetach(ctx context.Context, opts commandOptions, entry, title string) error {
	if err := r.refuseInLazyWorkspace("gs detach"); err != nil {
		return err
	}
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return userError("invalid_args", "detach requires a changeset", "Run gs detach <changeset>.")
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return err
	}
	if state.ActiveStackID == "" {
		return userError("no_active_stack", "no active dependency tree in workspace", "Run gs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	stackClient := corev1.NewChangesetStackServiceClient(conn)
	stack, err := stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return err
	}
	detachedEntry := stackEntryByChangesetSelector(stack, entry)
	if detachedEntry == nil {
		return userError("stack_entry_not_found", "changeset not found", "Run gs deps to inspect available changesets.")
	}
	if detachedEntry.ParentChangesetId == "" {
		return userError("cannot_detach_root", "cannot detach the root changeset", "Choose a dependent changeset, or keep using the existing dependency tree.")
	}
	if state.CurrentChangesetID != "" {
		currentSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, state.CurrentChangesetID)
		if err != nil {
			return err
		}
		edits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, currentSnapshot, false)
		if err != nil {
			return err
		}
		if changed := changedPathsFromEdits(edits); len(changed) > 0 {
			return userError("workspace_dirty", "workspace has local changes not captured in the active entry", "Run gs modify before detaching entries.")
		}
	}
	res, err := stackClient.DetachStackEntry(callCtx, &corev1.DetachStackEntryRequest{
		StackId:     state.ActiveStackID,
		ChangesetId: detachedEntry.ChangesetId,
		Title:       strings.TrimSpace(title),
	})
	if err != nil {
		return err
	}
	if res.DetachedStack == nil || res.DetachedStack.Id == "" {
		return userError("detach_failed", "detach did not return a detached dependency tree", "Retry the operation or inspect dependencies with gs deps.")
	}
	restacked, err := stackClient.Restack(callCtx, &corev1.RestackRequest{
		StackId:            res.DetachedStack.Id,
		StartChangesetId:   detachedEntry.ChangesetId,
		TargetBaseCommitId: res.DetachedStack.BaseCommitId,
	})
	if err != nil {
		return err
	}
	restackedIDs := make([]string, 0, len(restacked.Entries))
	for _, cs := range restacked.Entries {
		if cs != nil {
			restackedIDs = append(restackedIDs, cs.Id)
		}
	}
	detachedStack, err := stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: res.DetachedStack.Id})
	if err != nil {
		return err
	}
	state.ActiveStackID = detachedStack.Id
	state.CurrentChangesetID = detachedEntry.ChangesetId
	if finalEntry := stackEntryByChangesetSelector(detachedStack, detachedEntry.ChangesetId); finalEntry != nil && finalEntry.Changeset != nil {
		state.CurrentPatchsetID = finalEntry.Changeset.CurrentPatchsetId
	}
	targetSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, detachedStack, detachedEntry.ChangesetId)
	if err != nil {
		return err
	}
	if err := r.materializeWorkspaceSnapshot(ctx, conn, cfg, ws, targetSnapshot); err != nil {
		return err
	}
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
		out := stackOutputFromProto(detachedStack)
		out.RestackedChangesets = restackedIDs
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "detached dependencies: %s\n", detachedStack.Id)
	printStack(r.Stdout, detachedStack)
	if len(restackedIDs) > 0 {
		fmt.Fprintf(r.Stdout, "updated: %s\n", strings.Join(restackedIDs, ", "))
	}
	return nil
}

func (r Runner) runStackInsert(ctx context.Context, opts commandOptions, parentSelector, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return userError("invalid_args", "insert requires --message", "Run gs insert --base <changeset> --message <title>.")
	}
	parentSelector = strings.TrimSpace(parentSelector)
	if parentSelector == "" {
		return userError("invalid_args", "insert requires --base", "Run gs insert --base <changeset> --message <title>.")
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return err
	}
	if state.ActiveStackID == "" {
		return userError("no_active_stack", "no active dependency tree in workspace", "Run gs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	stackClient := corev1.NewChangesetStackServiceClient(conn)
	changesetClient := corev1.NewChangesetServiceClient(conn)
	stack, err := stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return err
	}
	parentEntry := stackEntryByChangesetSelector(stack, parentSelector)
	if parentEntry == nil {
		return userError("stack_entry_not_found", "base changeset not found", "Run gs deps to inspect available changesets.")
	}
	parent, err := changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: parentEntry.ChangesetId})
	if err != nil {
		return err
	}
	if parent.CurrentPatchsetId == "" {
		return userError("parent_has_no_patchset", "base changeset has no current patchset", "Run gs modify on the base changeset before inserting a dependent.")
	}
	base, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, parent.Id)
	if err != nil {
		return err
	}
	edits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, base, true)
	if err != nil {
		return err
	}
	inserted, err := stackClient.AddStackEntry(callCtx, &corev1.AddStackEntryRequest{
		StackId:           state.ActiveStackID,
		Title:             title,
		ParentChangesetId: parent.Id,
		ParentPatchsetId:  parent.CurrentPatchsetId,
	})
	if err != nil {
		return err
	}
	patchset, err := changesetClient.UpdateChangeset(callCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:              inserted.Id,
		BaseCommitId:             state.BaseCommitID,
		BaseKind:                 "patchset",
		BasePatchsetId:           parent.CurrentPatchsetId,
		ExpectedParentPatchsetId: parent.CurrentPatchsetId,
		FileEdits:                edits,
	})
	if err != nil {
		return err
	}
	if activeEntry := stackEntryByChangesetSelector(stack, state.CurrentChangesetID); activeEntry != nil && activeEntry.ParentChangesetId == parent.Id {
		if _, err := stackClient.ReparentStackEntry(callCtx, &corev1.ReparentStackEntryRequest{
			StackId:              state.ActiveStackID,
			ChangesetId:          activeEntry.ChangesetId,
			NewParentChangesetId: inserted.Id,
			NewParentPatchsetId:  patchset.Id,
			SiblingOrder:         1,
		}); err != nil {
			return err
		}
		if _, err := stackClient.Restack(callCtx, &corev1.RestackRequest{
			StackId:          state.ActiveStackID,
			StartChangesetId: inserted.Id,
		}); err != nil {
			return err
		}
	}
	state.CurrentChangesetID = inserted.Id
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{
			ChangesetID:    inserted.Id,
			PatchsetID:     patchset.Id,
			PatchsetNumber: patchset.Number,
			StackID:        state.ActiveStackID,
			WebURL:         webResourceURL("/cs/" + firstNonEmpty(displayChangesetID(inserted), inserted.Id)),
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "inserted %s patchset %d\n", firstNonEmpty(displayChangesetID(inserted), inserted.Id), patchset.Number)
	return nil
}
