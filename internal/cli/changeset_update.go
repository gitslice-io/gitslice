package cli

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) runChangesetUpdate(ctx context.Context, opts commandOptions) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if state.CurrentChangesetID == "" {
		return userError("no_current_changeset", "no current changeset in workspace", "Run gs cs create first.")
	}
	conflictState, hasConflicts, err := r.readWorkspaceConflictState()
	if err != nil {
		return err
	}
	clearConflicts := false
	if hasConflicts && conflictState.ChangesetID == state.CurrentChangesetID {
		unresolved, err := r.unresolvedWorkspaceConflictPaths(ws, conflictState)
		if err != nil {
			return err
		}
		if len(unresolved) > 0 {
			return userError("unresolved_conflicts", "workspace has unresolved sync conflicts", "Resolve conflict markers, then run gs cs update again.")
		}
		clearConflicts = true
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	edits, _, err := r.snapshotEdits(ctx, conn, cfg, ws, true)
	if err != nil {
		return err
	}
	previousPatchsetID := state.CurrentPatchsetID
	patchset, err := corev1.NewChangesetServiceClient(conn).UpdateChangeset(authContext(ctx, cfg), &corev1.UpdateChangesetRequest{
		ChangesetId:               state.CurrentChangesetID,
		ExpectedCurrentPatchsetId: state.CurrentPatchsetID,
		BaseCommitId:              state.BaseCommitID,
		FileEdits:                 edits,
		ConversationId:            ws.ConversationID,
	})
	if err != nil {
		return err
	}
	label := firstNonEmpty(storage.ShortChangesetID(state.CurrentChangesetID), state.CurrentChangesetID)
	webURL := webResourceURL("/cs/" + label)
	if previousPatchsetID != "" && patchset.Id == previousPatchsetID {
		if opts.jsonOutput() {
			return r.writeJSONOutput(opts, changesetOutput{
				ChangesetID:    state.CurrentChangesetID,
				PatchsetID:     patchset.Id,
				PatchsetNumber: patchset.Number,
				WebURL:         webURL,
			})
		}
		if !opts.Quiet {
			fmt.Fprintln(r.Stdout, "no changes since last patchset")
		}
		return nil
	}
	state.CurrentPatchsetID = patchset.Id
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if clearConflicts {
		if err := r.clearWorkspaceConflictState(); err != nil {
			return err
		}
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{
			ChangesetID:    state.CurrentChangesetID,
			PatchsetID:     patchset.Id,
			PatchsetNumber: patchset.Number,
			WebURL:         webURL,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "updated changeset %s patchset %d\n", label, patchset.Number)
	if webURL != "" {
		fmt.Fprintf(r.Stdout, "view: %s\n", webURL)
	}
	return nil
}

func (r Runner) runStackModify(ctx context.Context, opts commandOptions, message string, noRestack, all bool) error {
	_ = all
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return err
	}
	if state.CurrentChangesetID == "" {
		return userError("no_current_changeset", "no current changeset in workspace", "Run gs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	changesetClient := corev1.NewChangesetServiceClient(conn)
	cs, err := changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: state.CurrentChangesetID})
	if err != nil {
		return err
	}
	updateReq := &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: state.CurrentPatchsetID,
		BaseCommitId:              state.BaseCommitID,
	}
	var stack *corev1.ChangesetStack
	if cs.StackId != "" {
		stack, err = corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: cs.StackId})
		if err != nil {
			return err
		}
	}
	if cs.ParentChangesetId != "" {
		parent, err := changesetClient.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: cs.ParentChangesetId})
		if err != nil {
			return err
		}
		if parent.CurrentPatchsetId == "" {
			return userError("parent_has_no_patchset", "base changeset has no current patchset", "Run gs update-dependents after the parent has a patchset.")
		}
		updateReq.BaseKind = "patchset"
		updateReq.BasePatchsetId = parent.CurrentPatchsetId
		updateReq.ExpectedParentPatchsetId = parent.CurrentPatchsetId
	}
	base, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, cs.ParentChangesetId)
	if err != nil {
		return err
	}
	edits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, base, true)
	if err != nil {
		return err
	}
	updateReq.FileEdits = edits
	patchset, err := changesetClient.UpdateChangeset(callCtx, updateReq)
	if err != nil {
		return err
	}
	state.CurrentPatchsetID = patchset.Id
	if cs.StackId != "" {
		state.ActiveStackID = cs.StackId
	}
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	restacked := []string{}
	if cs.StackId != "" && !noRestack {
		res, err := corev1.NewChangesetStackServiceClient(conn).Restack(callCtx, &corev1.RestackRequest{
			StackId:          cs.StackId,
			StartChangesetId: cs.Id,
		})
		if err != nil {
			return err
		}
		for _, entry := range res.Entries {
			restacked = append(restacked, entry.Id)
		}
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{
			ChangesetID:    cs.Id,
			PatchsetID:     patchset.Id,
			PatchsetNumber: patchset.Number,
			StackID:        cs.StackId,
			WebURL:         webResourceURL("/cs/" + firstNonEmpty(displayChangesetID(cs), cs.Id)),
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "updated %s patchset %d\n", firstNonEmpty(displayChangesetID(cs), cs.Id), patchset.Number)
	if link := webResourceURL("/cs/" + firstNonEmpty(displayChangesetID(cs), cs.Id)); link != "" {
		fmt.Fprintf(r.Stdout, "view: %s\n", link)
	}
	if len(restacked) > 0 {
		fmt.Fprintf(r.Stdout, "updated dependents: %s\n", strings.Join(restacked, ", "))
	}
	_ = message
	return nil
}
