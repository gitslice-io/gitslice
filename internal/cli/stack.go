package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

func (r Runner) runStackShow(ctx context.Context, opts commandOptions, requestedStackID string) error {
	cfg, _, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return err
	}
	stackID := strings.TrimSpace(requestedStackID)
	if stackID == "" {
		stackID = state.ActiveStackID
	}
	if stackID == "" {
		return userError("no_active_stack", "no active dependency tree in workspace", "Run gs create first or pass a dependency tree id.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	stack, err := corev1.NewChangesetStackServiceClient(conn).GetStack(authContext(ctx, cfg), &corev1.GetStackRequest{StackId: stackID})
	if err != nil {
		return err
	}
	out := stackOutputFromProto(stack)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	printStack(r.Stdout, stack)
	return nil
}

func (r Runner) runStackRestack(ctx context.Context, opts commandOptions, entry string, children, all bool) error {
	if err := r.refuseInLazyWorkspace("gs restack"); err != nil {
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
	start := strings.TrimSpace(entry)
	if start == "" && children {
		start = state.CurrentChangesetID
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	stackClient := corev1.NewChangesetStackServiceClient(conn)
	res, err := stackClient.Restack(callCtx, &corev1.RestackRequest{
		StackId:          state.ActiveStackID,
		StartChangesetId: start,
		IncludeSiblings:  all,
	})
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(res.Entries))
	var firstConflict *corev1.Changeset
	var firstConflictPatchset *corev1.Patchset
	for _, cs := range res.Entries {
		ids = append(ids, cs.Id)
		if cs.Id == state.CurrentChangesetID {
			state.CurrentPatchsetID = cs.CurrentPatchsetId
		}
		if firstConflict == nil {
			if patchset := currentPatchset(cs); patchset != nil && len(patchset.Conflicts) > 0 {
				firstConflict = cs
				firstConflictPatchset = patchset
			}
		}
	}
	if firstConflict != nil && firstConflictPatchset != nil {
		state.CurrentChangesetID = firstConflict.Id
		state.CurrentPatchsetID = firstConflictPatchset.Id
		conflictState := WorkspaceConflictState{
			OldBaseCommitID: firstNonEmpty(firstConflictPatchset.BaseCommitId, state.BaseCommitID),
			NewBaseCommitID: firstNonEmpty(firstConflictPatchset.BaseCommitId, state.BaseCommitID),
			ChangesetID:     firstConflict.Id,
			PatchsetID:      firstConflictPatchset.Id,
			Conflicts:       workspaceConflictsFromProto(firstConflictPatchset.Conflicts),
		}
		if err := r.writeWorkspaceConflictState(conflictState); err != nil {
			return err
		}
		stack, err := stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
		if err != nil {
			return err
		}
		targetSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, firstConflict.Id)
		if err != nil {
			return err
		}
		if err := r.materializeWorkspaceSnapshot(ctx, conn, cfg, ws, targetSnapshot); err != nil {
			return err
		}
		if err := r.materializeStackRestackConflicts(ctx, conn, cfg, ws, conflictState.Conflicts); err != nil {
			return err
		}
	}
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, stackOutput{StackID: res.StackId, Status: res.Status, RestackedChangesets: ids})
	}
	if opts.Quiet {
		return nil
	}
	if len(ids) == 0 {
		fmt.Fprintln(r.Stdout, "nothing to update")
		return nil
	}
	fmt.Fprintf(r.Stdout, "updated: %s\n", strings.Join(ids, ", "))
	if firstConflict != nil {
		fmt.Fprintf(r.Stdout, "conflicts: %s\n", firstNonEmpty(storage.ShortChangesetID(firstConflict.Id), firstConflict.Id))
	}
	return nil
}

func (r Runner) runStackSwitch(ctx context.Context, opts commandOptions, entry string) error {
	if err := r.refuseInLazyWorkspace("Switching changesets"); err != nil {
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
	stack, err := corev1.NewChangesetStackServiceClient(conn).GetStack(authContext(ctx, cfg), &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return err
	}
	stackEntry := stackEntryByChangesetSelector(stack, entry)
	if stackEntry == nil || stackEntry.Changeset == nil {
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
			return userError("workspace_dirty", "workspace has local changes not captured in the active entry", "Run gs modify before switching entries.")
		}
	}
	targetSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, stack, stackEntry.ChangesetId)
	if err != nil {
		return err
	}
	if err := r.materializeWorkspaceSnapshot(ctx, conn, cfg, ws, targetSnapshot); err != nil {
		return err
	}
	state.CurrentChangesetID = stackEntry.ChangesetId
	state.CurrentPatchsetID = stackEntry.Changeset.CurrentPatchsetId
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, changesetOutput{
			ChangesetID:    state.CurrentChangesetID,
			PatchsetID:     state.CurrentPatchsetID,
			PatchsetNumber: stackEntry.Changeset.CurrentPatchsetNumber,
			StackID:        state.ActiveStackID,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "active: %s\n", firstNonEmpty(displayChangesetID(stackEntry.Changeset), stackEntry.ChangesetId))
	return nil
}

func (r Runner) runStackUp(ctx context.Context, opts commandOptions, entry string) error {
	_, _, state, stack, err := r.activeStack(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(entry) != "" {
		return r.runStackSwitch(ctx, opts, entry)
	}
	children := stackChildren(stack, state.CurrentChangesetID)
	if len(children) == 0 {
		return userError("stack_entry_not_found", "active changeset has no dependents", "Run gs deps to inspect available changesets.")
	}
	if len(children) > 1 {
		return userError("ambiguous_stack_navigation", "active changeset has multiple dependents", "Run gs up <changeset> with the dependent changeset to activate.")
	}
	return r.runStackSwitch(ctx, opts, children[0].ChangesetId)
}

func (r Runner) runStackDown(ctx context.Context, opts commandOptions, steps int) error {
	_, _, state, stack, err := r.activeStack(ctx)
	if err != nil {
		return err
	}
	entry := stackEntryByChangesetSelector(stack, state.CurrentChangesetID)
	if entry == nil {
		return userError("stack_entry_not_found", "active changeset not found", "Run gs deps to inspect available changesets.")
	}
	for i := 0; i < steps && entry.ParentChangesetId != ""; i++ {
		parent := stackEntryByChangesetSelector(stack, entry.ParentChangesetId)
		if parent == nil {
			break
		}
		entry = parent
	}
	return r.runStackSwitch(ctx, opts, entry.ChangesetId)
}

func (r Runner) runStackTop(ctx context.Context, opts commandOptions) error {
	_, _, state, stack, err := r.activeStack(ctx)
	if err != nil {
		return err
	}
	entry := stackEntryByChangesetSelector(stack, state.CurrentChangesetID)
	if entry == nil {
		entry = stackEntryByChangesetSelector(stack, stack.RootEntryId)
	}
	if entry == nil {
		return userError("stack_entry_not_found", "dependency tree has no root changeset", "Run gs deps to inspect available changesets.")
	}
	for {
		children := stackChildren(stack, entry.ChangesetId)
		if len(children) == 0 {
			return r.runStackSwitch(ctx, opts, entry.ChangesetId)
		}
		if len(children) > 1 {
			return userError("ambiguous_stack_navigation", "dependency path has multiple dependents", "Run gs up <changeset> with the dependent changeset to activate.")
		}
		entry = children[0]
	}
}

func (r Runner) runStackBottom(ctx context.Context, opts commandOptions) error {
	_, _, _, stack, err := r.activeStack(ctx)
	if err != nil {
		return err
	}
	if stack.RootEntryId == "" {
		return userError("stack_entry_not_found", "dependency tree has no root changeset", "Run gs deps to inspect available changesets.")
	}
	return r.runStackSwitch(ctx, opts, stack.RootEntryId)
}

func (r Runner) activeStack(ctx context.Context) (UserConfig, WorkspaceConfig, WorkspaceState, *corev1.ChangesetStack, error) {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, nil, err
	}
	if err := rejectPreStackWorkspaceState(state); err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, nil, err
	}
	if state.ActiveStackID == "" {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, nil, userError("no_active_stack", "no active dependency tree in workspace", "Run gs create first.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, nil, err
	}
	defer conn.Close()
	stack, err := corev1.NewChangesetStackServiceClient(conn).GetStack(authContext(ctx, cfg), &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, nil, err
	}
	return cfg, ws, state, stack, nil
}

func (r Runner) materializeWorkspaceSnapshot(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, snapshot BaseSnapshot) error {
	if err := r.refuseInLazyWorkspace("Rewriting the workspace's files"); err != nil {
		return err
	}
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	current, err := r.scanWorkspaceFiles(ws, snapshot.Files)
	if err != nil {
		return err
	}
	for p := range current {
		if _, ok := snapshot.Files[p]; !ok {
			if err := removeWorkspaceFile(root, ws, current, p); err != nil {
				return err
			}
		}
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	blob := corev1.NewBlobServiceClient(conn)
	paths := make([]string, 0, len(snapshot.Files))
	for p := range snapshot.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		file := snapshot.Files[p]
		if err := r.writeSnapshotFile(ctx, blob, cache, cfg, ws, root, file); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) writeSnapshotFile(ctx context.Context, blob corev1.BlobServiceClient, cache *clientcache.ObjectCache, cfg UserConfig, ws WorkspaceConfig, root string, file BaseSnapshotFile) error {
	if file.ContentHash == "" {
		return nil
	}
	rel := file.RelPath
	if rel == "" {
		var err error
		rel, err = workspaceRelPath(ws, file.Path)
		if err != nil {
			return err
		}
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if cache.Exists(file.ContentHash) {
		f, err := cache.Open(file.ContentHash)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = writeWorkspaceFile(target, file.Mode, f)
		return err
	}
	data, err := readBlobStreamBytes(authContext(ctx, cfg), blob, &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}, file.ContentHash, 0, 0)
	if err != nil {
		return err
	}
	cached, err := cache.PutBytes(data)
	if err != nil {
		return err
	}
	if cached.ContentHash != file.ContentHash {
		return fmt.Errorf("snapshot content hash mismatch for %s: got %s, want %s", file.Path, cached.ContentHash, file.ContentHash)
	}
	_, err = writeWorkspaceFile(target, file.Mode, bytes.NewReader(data))
	return err
}
