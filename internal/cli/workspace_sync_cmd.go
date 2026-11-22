package cli

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"strings"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type workspaceSyncOutput struct {
	Workspace            string   `json:"workspace"`
	PreviousBaseCommitID string   `json:"previous_base_commit_id"`
	NewBaseCommitID      string   `json:"new_base_commit_id"`
	Status               string   `json:"status"`
	UpdatedPathCount     int      `json:"updated_path_count"`
	UpdatedPaths         []string `json:"updated_paths"`
	LocalPathCount       int      `json:"local_path_count"`
	LocalPaths           []string `json:"local_paths"`
	MergedPathCount      int      `json:"merged_path_count"`
	MergedPaths          []string `json:"merged_paths"`
	ConflictCount        int      `json:"conflict_count"`
	ConflictPaths        []string `json:"conflict_paths"`
	MergeStrategy        string   `json:"merge_strategy"`
	ChangesetID          string   `json:"changeset_id,omitempty"`
	PatchsetID           string   `json:"patchset_id,omitempty"`
	PatchsetNumber       int64    `json:"patchset_number,omitempty"`
	RestackedChangesets  []string `json:"updated_changesets,omitempty"`
	LandedChangesetID    string   `json:"landed_changeset_id,omitempty"`
	LandedStackID        string   `json:"landed_stack_id,omitempty"`
	// BackupDir holds copies of the uncommitted edits a sync merged into,
	// taken before it changed them (a checkout with no changeset).
	BackupDir       string                 `json:"backup_dir,omitempty"`
	OverlapWarnings []overlapWarningOutput `json:"overlap_warnings,omitempty"`
}

const (
	defaultWorkspaceSyncMergeStrategy = "line"
	workspaceSyncMergeLine            = "line"
	workspaceSyncMergeManual          = "manual"
	workspaceSyncMergeOurs            = "ours"
	workspaceSyncMergeTheirs          = "theirs"

	blobStreamThresholdBytes = int64(4 << 20)
	blobStreamChunkBytes     = 1 << 20
)

func (r Runner) runWorkspaceSync(ctx context.Context, opts commandOptions, rawMergeStrategy string) error {
	mergeStrategy, err := normalizeWorkspaceSyncMergeStrategy(rawMergeStrategy)
	if err != nil {
		return err
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	metaStore, err := r.openWorkspaceMetadataStore()
	if err != nil {
		return err
	}
	ws, state, base := metaStore.Config, metaStore.State, metaStore.Snapshot
	oldBaseCommitID := firstNonEmpty(state.BaseCommitID, base.CommitID, ws.BaseCommitID)
	if oldBaseCommitID == "" {
		return userError("invalid_workspace_state", "workspace has no base commit", "Run gs init <slice|account:slice> again.")
	}
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	repo := corev1.NewRepositoryServiceClient(conn)
	ref, err := repo.GetRef(callCtx, &corev1.GetRefRequest{RefName: model.DefaultTargetRef})
	if err != nil {
		return err
	}
	newBaseCommitID := ref.CommitId

	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	driver, err := r.newWorkspaceSyncDriver(root)
	if err != nil {
		return err
	}
	current, err := driver.workingFiles(ctx, conn, cfg, ws, base)
	if err != nil {
		return err
	}

	localDirtyPaths := changedPathsFromSnapshot(base, current)
	advisoryDirtyPaths := append([]string(nil), localDirtyPaths...)
	var activeStack *corev1.ChangesetStack
	var landedChangesetID, landedStackID string
	if state.ActiveStackID != "" {
		activeStack, err = corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
		if err != nil {
			return err
		}
		landed, err := stackLanded(callCtx, corev1.NewChangesetServiceClient(conn), activeStack, state.CurrentChangesetID)
		if err != nil {
			return err
		}
		snapshotStack := activeStack
		if landed {
			// Landing rebased the closed tree onto the head it landed on, so
			// its base no longer matches the workspace's. The workspace still
			// holds the changesets' edits on its own base: compare with those.
			snapshotStack = proto.Clone(activeStack).(*corev1.ChangesetStack)
			snapshotStack.BaseCommitId = ""
		}
		activeSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, snapshotStack, state.CurrentChangesetID)
		if err != nil {
			return err
		}
		activeEdits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, activeSnapshot, false)
		if err != nil {
			return err
		}
		if changed := changedPathsFromEdits(activeEdits); len(changed) > 0 {
			return userError(
				"sync_needs_modify",
				"workspace has local changes not captured in the active changeset",
				"Run gs modify before syncing dependencies.",
			)
		}
		current = workingFilesFromSnapshot(base)
		localDirtyPaths = nil
		if landed {
			// The workspace's changesets are part of the head's history now:
			// move to the head and forget the closed tree. Restacking it
			// would only move the closed tree's base.
			landedChangesetID, landedStackID = state.CurrentChangesetID, state.ActiveStackID
			activeStack = nil
			state.ActiveStackID, state.CurrentChangesetID, state.CurrentPatchsetID = "", "", ""
		}
	}
	changesetClient := corev1.NewChangesetServiceClient(conn)
	var activeChangeset *corev1.Changeset
	if activeStack == nil {
		activeChangeset, err = r.activeWorkspaceChangeset(callCtx, changesetClient, state)
		if err != nil {
			return err
		}
	}
	// Uncommitted edits with no changeset to hold them are merged with the
	// new head in place, as long as every file merges cleanly: conflicts are
	// recorded on a changeset, so those need gs create first.
	uncommitted := len(localDirtyPaths) > 0 && activeChangeset == nil
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	remoteFiles, err := driver.remoteFiles(ctx, callCtx, conn, cfg, repo, ws, newBaseCommitID)
	if err != nil {
		return err
	}
	plan := buildWorkspaceSyncPlan(oldBaseCommitID, newBaseCommitID, base, current, remoteFiles)
	blobClient := corev1.NewBlobServiceClient(conn)
	sliceRef := &corev1.SliceRef{Account: ws.Account, Slice: ws.Slice}
	if err := prefetchSyncPlanBlobs(callCtx, repo, blobClient, sliceRef, cache, oldBaseCommitID, newBaseCommitID, base, remoteFiles, plan, driver.isLazy()); err != nil {
		return err
	}
	if err := r.applyWorkspaceSyncMergeStrategy(callCtx, repo, cache, oldBaseCommitID, newBaseCommitID, mergeStrategy, base, current, remoteFiles, &plan); err != nil {
		return err
	}
	backupDir := ""
	if uncommitted {
		if len(plan.Conflicts) > 0 {
			paths := workspaceConflictPaths(plan.Conflicts)
			return userError(
				"sync_needs_changeset",
				fmt.Sprintf("the latest commit conflicts with %d of your uncommitted edits (%s); nothing was changed", len(paths), strings.Join(paths, ", ")),
				"Run gs create --message <title> first, then run gs sync again: the conflicts are then recorded on the changeset.",
			)
		}
		if backupDir, err = driver.backupUncommitted(localDirtyPaths); err != nil {
			return err
		}
	}
	if err := driver.applySyncPlan(ctx, callCtx, repo, cache, ws, oldBaseCommitID, newBaseCommitID, base, current, remoteFiles, plan, activeStack != nil); err != nil {
		return err
	}

	remoteSnapshot := BaseSnapshot{CommitID: newBaseCommitID, Files: remoteBaseSnapshotFiles(remoteFiles)}
	output := workspaceSyncOutput{
		Workspace:            ws.Account + ":" + ws.Slice,
		PreviousBaseCommitID: oldBaseCommitID,
		NewBaseCommitID:      newBaseCommitID,
		Status:               "synced",
		UpdatedPaths:         plan.UpdatedPaths,
		UpdatedPathCount:     len(plan.UpdatedPaths),
		LocalPaths:           plan.LocalPaths,
		LocalPathCount:       len(plan.LocalPaths),
		MergedPaths:          plan.MergedPaths,
		MergedPathCount:      len(plan.MergedPaths),
		ConflictPaths:        workspaceConflictPaths(plan.Conflicts),
		ConflictCount:        len(plan.Conflicts),
		MergeStrategy:        mergeStrategy,
		LandedChangesetID:    landedChangesetID,
		LandedStackID:        landedStackID,
		BackupDir:            backupDir,
	}
	if oldBaseCommitID == newBaseCommitID && len(plan.UpdatedPaths) == 0 && len(plan.Conflicts) == 0 {
		output.Status = "up_to_date"
	}
	if len(plan.Conflicts) > 0 {
		output.Status = "conflicts"
	}

	var conflictUpdate *WorkspaceConflictState
	if activeChangeset != nil && (oldBaseCommitID != newBaseCommitID || len(plan.Conflicts) > 0) {
		edits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, remoteSnapshot, true)
		if err != nil {
			return err
		}
		patchset, err := changesetClient.UpdateChangeset(callCtx, &corev1.UpdateChangesetRequest{
			ChangesetId:               state.CurrentChangesetID,
			ExpectedCurrentPatchsetId: state.CurrentPatchsetID,
			BaseCommitId:              newBaseCommitID,
			FileEdits:                 edits,
			Conflicts:                 workspaceConflictsToProto(plan.Conflicts),
			PatchsetKind:              "sync",
		})
		if err != nil {
			return err
		}
		state.CurrentPatchsetID = patchset.Id
		output.ChangesetID = state.CurrentChangesetID
		output.PatchsetID = patchset.Id
		output.PatchsetNumber = patchset.Number
		if len(plan.Conflicts) > 0 {
			conflictUpdate = &WorkspaceConflictState{
				OldBaseCommitID: oldBaseCommitID,
				NewBaseCommitID: newBaseCommitID,
				ChangesetID:     state.CurrentChangesetID,
				PatchsetID:      patchset.Id,
				Conflicts:       plan.Conflicts,
			}
		} else {
			conflictUpdate = &WorkspaceConflictState{}
		}
	} else if len(plan.Conflicts) == 0 {
		conflictUpdate = &WorkspaceConflictState{}
	}

	if activeStack != nil && oldBaseCommitID != newBaseCommitID && len(plan.Conflicts) == 0 {
		stackClient := corev1.NewChangesetStackServiceClient(conn)
		restacked, err := stackClient.Restack(callCtx, &corev1.RestackRequest{
			StackId:            state.ActiveStackID,
			TargetBaseCommitId: newBaseCommitID,
		})
		if err != nil {
			return err
		}
		for _, cs := range restacked.Entries {
			if cs == nil {
				continue
			}
			output.RestackedChangesets = append(output.RestackedChangesets, cs.Id)
			if patchset := currentPatchset(cs); patchset != nil {
				for _, conflict := range patchset.GetConflicts() {
					output.ConflictPaths = append(output.ConflictPaths, conflict.GetPath())
					output.ConflictCount++
					output.Status = "conflicts"
				}
			}
			if cs.Id == state.CurrentChangesetID {
				state.CurrentPatchsetID = cs.CurrentPatchsetId
			}
		}
		activeStack, err = stackClient.GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
		if err != nil {
			return err
		}
		if err := driver.applyStackSnapshot(ctx, conn, cfg, ws, activeStack, &state, newBaseCommitID, remoteSnapshot); err != nil {
			return err
		}
	}

	// Commit .gs/{slice.json,base_snapshot.json,state.json,conflicts.json}
	// atomically only after all sync RPCs and snapshot materializations succeed.
	state.BaseCommitID = newBaseCommitID
	ws.BaseCommitID = newBaseCommitID
	if err := metaStore.CommitBase(ws, state, remoteSnapshot, conflictUpdate); err != nil {
		return err
	}

	if len(advisoryDirtyPaths) > 0 {
		output.OverlapWarnings = r.collectPathOverlapWarnings(
			ctx,
			conn,
			cfg,
			sliceRef,
			advisoryDirtyPaths,
			excludeChangesetIDsFromStack(state.CurrentChangesetID, activeStack),
		)
	}
	return r.writeWorkspaceSyncOutput(opts, oldBaseCommitID, newBaseCommitID, output)
}

func (r Runner) writeWorkspaceSyncOutput(opts commandOptions, oldBaseCommitID, newBaseCommitID string, output workspaceSyncOutput) error {
	if opts.JSONOutput() {
		if output.UpdatedPaths == nil {
			output.UpdatedPaths = []string{}
		}
		if output.LocalPaths == nil {
			output.LocalPaths = []string{}
		}
		if output.MergedPaths == nil {
			output.MergedPaths = []string{}
		}
		if output.ConflictPaths == nil {
			output.ConflictPaths = []string{}
		}
		if err := r.writeJSONOutput(opts, output); err != nil {
			return err
		}
		return syncConflictsError(output.ConflictCount)
	}
	if opts.Quiet {
		if output.Status == "up_to_date" {
			return nil
		}
		return userError("workspace_synced", "workspace sync changed local state", "Run gs status to inspect the workspace.")
	}
	fmt.Fprintf(r.Stdout, "synced %s from %s to %s\n", output.Workspace, shortID(oldBaseCommitID), shortID(newBaseCommitID))
	if output.LandedStackID != "" {
		landed := "dependency tree " + output.LandedStackID
		if output.LandedChangesetID != "" {
			landed = firstNonEmpty(model.ShortChangesetID(output.LandedChangesetID), output.LandedChangesetID)
		}
		fmt.Fprintf(r.Stdout, "%s landed; workspace is now on the latest commit\n", landed)
	}
	if output.UpdatedPathCount > 0 {
		fmt.Fprintf(r.Stdout, "updated %d remote path(s)\n", output.UpdatedPathCount)
	}
	if output.LocalPathCount > 0 {
		fmt.Fprintf(r.Stdout, "preserved %d local path(s)\n", output.LocalPathCount)
	}
	if output.MergedPathCount > 0 {
		fmt.Fprintf(r.Stdout, "merged %d path(s) with %s strategy\n", output.MergedPathCount, output.MergeStrategy)
	}
	if output.BackupDir != "" {
		fmt.Fprintf(r.Stdout, "your uncommitted edits were copied to %s first\n", output.BackupDir)
	}
	if output.PatchsetID != "" {
		fmt.Fprintf(r.Stdout, "created sync patchset %d for %s\n", output.PatchsetNumber, firstNonEmpty(model.ShortChangesetID(output.ChangesetID), output.ChangesetID))
	}
	if len(output.RestackedChangesets) > 0 {
		fmt.Fprintf(r.Stdout, "updated: %s\n", strings.Join(output.RestackedChangesets, ", "))
	}
	if output.ConflictCount > 0 {
		fmt.Fprintf(r.Stdout, "conflicts: %d path(s)\n", output.ConflictCount)
		for _, p := range output.ConflictPaths {
			fmt.Fprintf(r.Stdout, "  %s\n", p)
		}
	}
	printOverlapWarnings(r.Stdout, output.OverlapWarnings)
	return syncConflictsError(output.ConflictCount)
}

// syncConflictsError makes a sync that left conflicts fail (exit code 3),
// after its result is printed, so a script or agent that checks the exit
// code does not carry on as if the workspace were ready to submit.
func syncConflictsError(count int) error {
	if count == 0 {
		return nil
	}
	return userError("sync_conflicts", fmt.Sprintf("sync left %d path(s) with conflict markers", count), "Resolve the conflicts, then run gs modify.")
}

func (r Runner) activeWorkspaceChangeset(ctx context.Context, client corev1.ChangesetServiceClient, state WorkspaceState) (*corev1.Changeset, error) {
	if state.CurrentChangesetID == "" {
		return nil, nil
	}
	cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: state.CurrentChangesetID})
	if err != nil {
		if grpcstatus.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	if cs.Status == "" || cs.Status == "draft" {
		return cs, nil
	}
	return nil, nil
}

func stackLanded(ctx context.Context, client corev1.ChangesetServiceClient, stack *corev1.ChangesetStack, currentChangesetID string) (bool, error) {
	if stack == nil || len(stack.Entries) == 0 || stack.Status != "closed" {
		return false, nil
	}
	currentChangesetID = strings.TrimSpace(currentChangesetID)
	if currentChangesetID == "" {
		return true, nil
	}
	var current *corev1.Changeset
	for _, entry := range stack.Entries {
		if entry != nil && entry.ChangesetId == currentChangesetID {
			current = entry.Changeset
		}
	}
	if current == nil || current.Status == "" {
		var err error
		current, err = client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: currentChangesetID})
		switch grpcstatus.Code(err) {
		case codes.OK:
		case codes.NotFound, codes.PermissionDenied:
			return false, nil
		default:
			return false, err
		}
	}
	return current.Status == "submitted", nil
}
