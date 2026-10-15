package cli

import (
	"context"
	"fmt"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type statusOutput struct {
	Workspace          string       `json:"workspace"`
	ChangedPathCount   int          `json:"changed_path_count"`
	ChangedPaths       []string     `json:"changed_paths"`
	ConflictCount      int          `json:"conflict_count,omitempty"`
	ConflictPaths      []string     `json:"conflict_paths,omitempty"`
	CurrentChangesetID string       `json:"changeset_id,omitempty"`
	CurrentPatchsetID  string       `json:"patchset_id,omitempty"`
	StackID            string       `json:"dependency_id,omitempty"`
	Stack              *stackOutput `json:"dependency_tree,omitempty"`
}

func (r Runner) runStatus(ctx context.Context, opts commandOptions) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	var stack *corev1.ChangesetStack
	if state.ActiveStackID != "" {
		stack, err = corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
		if err != nil {
			return err
		}
	}
	activeBaseID := ""
	if stack != nil && state.CurrentChangesetID != "" {
		if entry := stackEntryByChangesetSelector(stack, state.CurrentChangesetID); entry != nil {
			activeBaseID = entry.ParentChangesetId
		}
	}
	base, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, activeBaseID)
	if err != nil {
		return err
	}
	edits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, base, false)
	if err != nil {
		return err
	}
	changedPaths := changedPathsFromEdits(edits)
	if stack == nil {
		validation, err := corev1.NewWorkspaceServiceClient(conn).ValidateWorkspaceDiff(callCtx, &corev1.ValidateWorkspaceDiffRequest{
			Workspace:    workspaceRef(ws),
			BaseCommitId: state.BaseCommitID,
			FileEdits:    edits,
		})
		if err != nil {
			return err
		}
		changedPaths = validation.AffectedPaths
	}
	conflictState, hasConflicts, err := r.readWorkspaceConflictState()
	if err != nil {
		return err
	}
	conflictPaths := []string{}
	if hasConflicts && conflictState.ChangesetID == state.CurrentChangesetID {
		conflictPaths = workspaceConflictStatePaths(conflictState)
	}
	output := statusOutput{
		Workspace:          ws.Account + ":" + ws.Slice,
		ChangedPathCount:   len(changedPaths),
		ChangedPaths:       changedPaths,
		ConflictCount:      len(conflictPaths),
		ConflictPaths:      conflictPaths,
		CurrentChangesetID: state.CurrentChangesetID,
		CurrentPatchsetID:  state.CurrentPatchsetID,
	}
	if stack != nil {
		stackOut := stackOutputFromProto(stack)
		output.StackID = stack.Id
		output.Stack = &stackOut
	}
	if opts.jsonOutput() {
		if output.ChangedPaths == nil {
			output.ChangedPaths = []string{}
		}
		return r.writeJSONOutput(opts, output)
	}
	if opts.Quiet {
		if output.ChangedPathCount == 0 && output.ConflictCount == 0 {
			return nil
		}
		return userError("workspace_dirty", "workspace has local changes", "Run gs status without --quiet to list changed paths.")
	}
	fmt.Fprintf(r.Stdout, "workspace: %s\n", output.Workspace)
	if stack != nil {
		fmt.Fprintf(r.Stdout, "dependencies: %s\n", stack.Id)
		if state.CurrentChangesetID != "" {
			fmt.Fprintf(r.Stdout, "active: %s\n", firstNonEmpty(storage.ShortChangesetID(state.CurrentChangesetID), state.CurrentChangesetID))
		}
		printStack(r.Stdout, stack)
	}
	if output.ChangedPathCount == 0 && output.ConflictCount == 0 {
		fmt.Fprintln(r.Stdout, "status: clean")
		return nil
	}
	fmt.Fprintf(r.Stdout, "status: %d changed path(s)\n", output.ChangedPathCount)
	if output.ConflictCount > 0 {
		fmt.Fprintf(r.Stdout, "conflicts: %d unresolved path(s)\n", output.ConflictCount)
		for _, p := range output.ConflictPaths {
			fmt.Fprintf(r.Stdout, "  %s\n", p)
		}
	}
	for _, p := range output.ChangedPaths {
		fmt.Fprintf(r.Stdout, "  %s\n", p)
	}
	return nil
}
