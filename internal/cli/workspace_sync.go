package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
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

func (r Runner) runWorkspaceSync(ctx context.Context, opts commandOptions) error {
	if err := r.refuseInLazyWorkspace("gs sync"); err != nil {
		return err
	}
	mergeStrategy, err := normalizeWorkspaceSyncMergeStrategy(opts.SyncMerge)
	if err != nil {
		return err
	}
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	base, err := r.readBaseSnapshot()
	if err != nil {
		return err
	}
	oldBaseCommitID := firstNonEmpty(state.BaseCommitID, base.CommitID, ws.BaseCommitID)
	if oldBaseCommitID == "" {
		return userError("invalid_workspace_state", "workspace has no base commit", "Run gs init <slice|account:slice> again.")
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	repo := corev1.NewRepositoryServiceClient(conn)
	ref, err := repo.GetRef(callCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	newBaseCommitID := ref.CommitId
	current, err := r.scanWorkspaceFiles(ws)
	if err != nil {
		return err
	}
	localDirtyPaths := changedPathsFromSnapshot(base, current)
	var activeStack *corev1.ChangesetStack
	if state.ActiveStackID != "" {
		activeStack, err = corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
		if err != nil {
			return err
		}
		activeSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, activeStack, state.CurrentChangesetID)
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
	}
	changesetClient := corev1.NewChangesetServiceClient(conn)
	var activeChangeset *corev1.Changeset
	if activeStack == nil {
		activeChangeset, err = r.activeWorkspaceChangeset(callCtx, changesetClient, state)
		if err != nil {
			return err
		}
	}
	if len(localDirtyPaths) > 0 && activeChangeset == nil {
		return userError(
			"sync_needs_changeset",
			"workspace has local changes but no draft changeset",
			"Run gs create --message <title> first, then run gs sync again.",
		)
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	remoteFiles, err := r.remoteWorkspaceFiles(callCtx, repo, ws, newBaseCommitID)
	if err != nil {
		return err
	}
	plan := buildWorkspaceSyncPlan(oldBaseCommitID, newBaseCommitID, base, current, remoteFiles)
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	if err := r.applyWorkspaceSyncMergeStrategy(callCtx, repo, cache, oldBaseCommitID, newBaseCommitID, mergeStrategy, base, current, remoteFiles, &plan); err != nil {
		return err
	}
	progress := startProgress(ctx, "syncing", "paths", len(plan.RemoteDeletes)+len(plan.RemoteUpserts)+len(plan.MergedFiles)+len(plan.Conflicts))
	for _, p := range plan.RemoteDeletes {
		if err := removeWorkspaceFile(root, ws, current, p); err != nil {
			progress.Finish()
			return err
		}
		progress.Add(1)
	}
	for _, file := range plan.RemoteUpserts {
		if err := r.writeWorkspaceRemoteFile(callCtx, repo, cache, root, ws, current, newBaseCommitID, file); err != nil {
			progress.Finish()
			return err
		}
		progress.Add(1)
	}
	for _, file := range plan.MergedFiles {
		if err := r.writeWorkspaceMergedFile(root, ws, current, file); err != nil {
			progress.Finish()
			return err
		}
		progress.Add(1)
	}
	for _, conflict := range plan.Conflicts {
		if err := r.materializeWorkspaceConflict(callCtx, repo, cache, root, ws, oldBaseCommitID, newBaseCommitID, base, current, remoteFiles, conflict); err != nil {
			progress.Finish()
			return err
		}
		progress.Add(1)
	}
	progress.Finish()
	remoteSnapshot := BaseSnapshot{CommitID: newBaseCommitID, Files: remoteBaseSnapshotFiles(remoteFiles)}
	state.BaseCommitID = newBaseCommitID
	ws.BaseCommitID = newBaseCommitID
	if err := r.writeWorkspaceConfig(ws); err != nil {
		return err
	}
	if err := r.writeBaseSnapshot(remoteSnapshot); err != nil {
		return err
	}
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
	}
	if oldBaseCommitID == newBaseCommitID && len(plan.UpdatedPaths) == 0 && len(plan.Conflicts) == 0 {
		output.Status = "up_to_date"
	}
	if len(plan.Conflicts) > 0 {
		output.Status = "conflicts"
	}
	if activeChangeset != nil && (oldBaseCommitID != newBaseCommitID || len(plan.Conflicts) > 0) {
		edits, _, err := r.snapshotEdits(ctx, conn, cfg, ws, true)
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
			if err := r.writeWorkspaceConflictState(WorkspaceConflictState{
				OldBaseCommitID: oldBaseCommitID,
				NewBaseCommitID: newBaseCommitID,
				ChangesetID:     state.CurrentChangesetID,
				PatchsetID:      patchset.Id,
				Conflicts:       plan.Conflicts,
			}); err != nil {
				return err
			}
		} else if err := r.clearWorkspaceConflictState(); err != nil {
			return err
		}
	} else if len(plan.Conflicts) == 0 {
		if err := r.clearWorkspaceConflictState(); err != nil {
			return err
		}
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
			// Files both the changeset and the new head changed, in ways the
			// restack could not merge, carry conflict markers.
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
		if state.CurrentChangesetID != "" {
			if entry := stackEntryByChangesetSelector(activeStack, state.CurrentChangesetID); entry != nil && entry.Changeset != nil {
				state.CurrentPatchsetID = entry.Changeset.CurrentPatchsetId
			}
			activeStack.BaseCommitId = newBaseCommitID
			targetSnapshot, err := r.stackBaseSnapshotThroughChangeset(ctx, conn, cfg, ws, activeStack, state.CurrentChangesetID)
			if err != nil {
				return err
			}
			if err := r.materializeWorkspaceSnapshot(ctx, conn, cfg, ws, targetSnapshot); err != nil {
				return err
			}
		}
	}
	if err := r.writeWorkspaceState(state); err != nil {
		return err
	}
	if opts.jsonOutput() {
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
		return r.writeJSONOutput(opts, output)
	}
	if opts.Quiet {
		if output.Status == "up_to_date" {
			return nil
		}
		return userError("workspace_synced", "workspace sync changed local state", "Run gs status to inspect the workspace.")
	}
	fmt.Fprintf(r.Stdout, "synced %s from %s to %s\n", output.Workspace, shortID(oldBaseCommitID), shortID(newBaseCommitID))
	if output.UpdatedPathCount > 0 {
		fmt.Fprintf(r.Stdout, "updated %d remote path(s)\n", output.UpdatedPathCount)
	}
	if output.LocalPathCount > 0 {
		fmt.Fprintf(r.Stdout, "preserved %d local path(s)\n", output.LocalPathCount)
	}
	if output.MergedPathCount > 0 {
		fmt.Fprintf(r.Stdout, "merged %d path(s) with %s strategy\n", output.MergedPathCount, output.MergeStrategy)
	}
	if output.PatchsetID != "" {
		fmt.Fprintf(r.Stdout, "created sync patchset %d for %s\n", output.PatchsetNumber, firstNonEmpty(storage.ShortChangesetID(output.ChangesetID), output.ChangesetID))
	}
	if len(output.RestackedChangesets) > 0 {
		fmt.Fprintf(r.Stdout, "updated: %s\n", strings.Join(output.RestackedChangesets, ", "))
	}
	if output.ConflictCount > 0 {
		fmt.Fprintf(r.Stdout, "conflicts: %d path(s)\n", output.ConflictCount)
		for _, p := range output.ConflictPaths {
			fmt.Fprintf(r.Stdout, "  %s\n", p)
		}
		fmt.Fprintln(r.Stdout, "resolve conflicts, then run gs modify")
	}
	return nil
}

type remoteWorkspaceFile struct {
	BaseSnapshotFile
	Entry *corev1.TreeEntry
}

type mergedWorkspaceFile struct {
	Path string
	Data []byte
	Mode uint32
}

type workspaceSyncPlan struct {
	RemoteUpserts []remoteWorkspaceFile
	RemoteDeletes []string
	MergedFiles   []mergedWorkspaceFile
	UpdatedPaths  []string
	LocalPaths    []string
	MergedPaths   []string
	Conflicts     []WorkspaceConflict
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

func buildWorkspaceSyncPlan(oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile) workspaceSyncPlan {
	pathSet := map[string]struct{}{}
	for p := range base.Files {
		pathSet[p] = struct{}{}
	}
	for p := range current {
		pathSet[p] = struct{}{}
	}
	for p := range remote {
		pathSet[p] = struct{}{}
	}
	allPaths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		allPaths = append(allPaths, p)
	}
	sort.Strings(allPaths)

	plan := workspaceSyncPlan{}
	for _, p := range allPaths {
		baseFile, baseOK := base.Files[p]
		localFile, localOK := current[p]
		remoteFile, remoteOK := remote[p]
		localSnapshot := localFile.BaseSnapshotFile
		remoteSnapshot := remoteFile.BaseSnapshotFile
		localChanged := !sameSnapshotFile(baseFile, baseOK, localSnapshot, localOK)
		remoteChanged := !sameSnapshotFile(baseFile, baseOK, remoteSnapshot, remoteOK)
		switch {
		case !localChanged && remoteChanged:
			plan.UpdatedPaths = append(plan.UpdatedPaths, p)
			if remoteOK {
				plan.RemoteUpserts = append(plan.RemoteUpserts, remoteFile)
			} else {
				plan.RemoteDeletes = append(plan.RemoteDeletes, p)
			}
		case localChanged && remoteChanged:
			if sameSnapshotFile(localSnapshot, localOK, remoteSnapshot, remoteOK) {
				plan.UpdatedPaths = append(plan.UpdatedPaths, p)
				continue
			}
			plan.LocalPaths = append(plan.LocalPaths, p)
			plan.Conflicts = append(plan.Conflicts, workspaceConflict(oldBaseCommitID, newBaseCommitID, p, baseFile, baseOK, localSnapshot, localOK, remoteSnapshot, remoteOK))
		case localChanged:
			plan.LocalPaths = append(plan.LocalPaths, p)
		}
	}
	return plan
}

func normalizeWorkspaceSyncMergeStrategy(strategy string) (string, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		return defaultWorkspaceSyncMergeStrategy, nil
	}
	switch strategy {
	case workspaceSyncMergeLine, workspaceSyncMergeManual, workspaceSyncMergeOurs, workspaceSyncMergeTheirs:
		return strategy, nil
	default:
		return "", userError("invalid_args", "invalid sync merge strategy "+strategy, "Use --merge line, --merge manual, --merge ours, or --merge theirs.")
	}
}

func (r Runner) applyWorkspaceSyncMergeStrategy(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, oldBaseCommitID, newBaseCommitID, strategy string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, plan *workspaceSyncPlan) error {
	if len(plan.Conflicts) == 0 {
		return nil
	}
	if strategy == workspaceSyncMergeManual {
		return nil
	}
	remaining := make([]WorkspaceConflict, 0, len(plan.Conflicts))
	for _, conflict := range plan.Conflicts {
		switch strategy {
		case workspaceSyncMergeOurs:
			continue
		case workspaceSyncMergeTheirs:
			remoteFile, remoteOK := remote[conflict.Path]
			if remoteOK {
				plan.RemoteUpserts = append(plan.RemoteUpserts, remoteFile)
			} else {
				plan.RemoteDeletes = append(plan.RemoteDeletes, conflict.Path)
			}
			plan.UpdatedPaths = appendUniqueString(plan.UpdatedPaths, conflict.Path)
			plan.LocalPaths = removeStringValue(plan.LocalPaths, conflict.Path)
		case workspaceSyncMergeLine:
			merged, ok, err := r.tryLineMergeWorkspaceConflict(ctx, repo, cache, oldBaseCommitID, newBaseCommitID, base, current, remote, conflict)
			if err != nil {
				return err
			}
			if !ok {
				remaining = append(remaining, conflict)
				continue
			}
			plan.MergedFiles = append(plan.MergedFiles, merged)
			plan.MergedPaths = appendUniqueString(plan.MergedPaths, conflict.Path)
		}
	}
	plan.Conflicts = remaining
	sort.Strings(plan.LocalPaths)
	sort.Strings(plan.UpdatedPaths)
	sort.Strings(plan.MergedPaths)
	return nil
}

func (r Runner) tryLineMergeWorkspaceConflict(ctx context.Context, repo corev1.RepositoryServiceClient, cache *clientcache.ObjectCache, oldBaseCommitID, newBaseCommitID string, base BaseSnapshot, current map[string]workingFile, remote map[string]remoteWorkspaceFile, conflict WorkspaceConflict) (mergedWorkspaceFile, bool, error) {
	baseFile, baseOK := base.Files[conflict.Path]
	localFile, localOK := current[conflict.Path]
	remoteFile, remoteOK := remote[conflict.Path]
	if !baseOK || !localOK || !remoteOK {
		return mergedWorkspaceFile{}, false, nil
	}
	baseMode := normalizedSnapshotMode(baseFile.Mode)
	localMode := normalizedSnapshotMode(localFile.Mode)
	remoteMode := normalizedSnapshotMode(remoteFile.Mode)
	if baseMode != localMode || baseMode != remoteMode {
		return mergedWorkspaceFile{}, false, nil
	}
	baseData, baseExists, err := r.remoteFileBytes(ctx, repo, cache, oldBaseCommitID, baseFile, true)
	if err != nil {
		return mergedWorkspaceFile{}, false, err
	}
	localData, localExists, err := localWorkspaceFileBytes(localFile, true)
	if err != nil {
		return mergedWorkspaceFile{}, false, err
	}
	remoteData, remoteExists, err := r.remoteFileBytes(ctx, repo, cache, newBaseCommitID, remoteFile.BaseSnapshotFile, true)
	if err != nil {
		return mergedWorkspaceFile{}, false, err
	}
	if !baseExists || !localExists || !remoteExists {
		return mergedWorkspaceFile{}, false, nil
	}
	if !isTextConflictSide(baseData, true) || !isTextConflictSide(localData, true) || !isTextConflictSide(remoteData, true) {
		return mergedWorkspaceFile{}, false, nil
	}
	merged, ok := mergeTextLines(baseData, localData, remoteData)
	if !ok {
		return mergedWorkspaceFile{}, false, nil
	}
	return mergedWorkspaceFile{
		Path: conflict.Path,
		Data: merged,
		Mode: localMode,
	}, true, nil
}
