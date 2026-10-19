package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type statusOutput struct {
	Workspace          string       `json:"workspace"`
	ChangedPathCount   int          `json:"changed_path_count"`
	ChangedPaths       []string     `json:"changed_paths"`
	Changes            []pathChange `json:"changes"`
	LinesAdded         int          `json:"lines_added"`
	LinesRemoved       int          `json:"lines_removed"`
	ConflictCount      int          `json:"conflict_count,omitempty"`
	ConflictPaths      []string     `json:"conflict_paths,omitempty"`
	CurrentChangesetID string       `json:"changeset_id,omitempty"`
	CurrentPatchsetID  string       `json:"patchset_id,omitempty"`
	StackID            string       `json:"dependency_id,omitempty"`
	Stack              *stackOutput `json:"dependency_tree,omitempty"`
	// Offline is set when the server could not be reached: the edits are
	// still this workspace's own, and a dependency tree is the last one seen.
	Offline bool `json:"offline,omitempty"`
}

// statusServerTimeout bounds each server call gs status makes, so an
// unreachable server makes it answer from local state instead of hanging.
var statusServerTimeout = 5 * time.Second

// isOfflineError reports whether err means the server could not be reached.
func isOfflineError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch grpcstatus.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

// localEdits are a workspace's local edits against its base: the base
// the active changeset builds on, which includes the changesets below it.
type localEdits struct {
	stack   *corev1.ChangesetStack
	base    BaseSnapshot
	edits   []*corev1.FileEdit
	current map[string]workingFile
	offline bool
}

// loadWorkspaceEdits finds the workspace's edits. It needs the server only
// for the dependency tree; offline it uses the tree it last saw, kept in
// .gs/dependency-tree.json, and fails only when it has none and the active
// changeset builds on others.
func (r Runner) loadWorkspaceEdits(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, ws WorkspaceConfig, state WorkspaceState) (localEdits, error) {
	var out localEdits
	if state.ActiveStackID != "" {
		callCtx, cancel := context.WithTimeout(authContext(ctx, cfg), statusServerTimeout)
		stack, err := corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
		cancel()
		switch {
		case err == nil:
			out.stack = stack
		case isOfflineError(err):
			out.offline = true
			out.stack = r.readCachedDependencyTree(state.ActiveStackID)
		default:
			return out, err
		}
	}
	activeBaseID := ""
	if out.stack != nil && state.CurrentChangesetID != "" {
		if entry := stackEntryByChangesetSelector(out.stack, state.CurrentChangesetID); entry != nil {
			activeBaseID = entry.ParentChangesetId
		}
	}
	if out.stack == nil && out.offline && state.CurrentChangesetID != "" {
		return out, userError("offline", "the server is unreachable, and the dependency tree of this workspace's changesets is not on this machine yet",
			"Run gs status once while online; after that it works offline.")
	}
	baseCtx, cancel := context.WithTimeout(ctx, statusServerTimeout)
	base, err := r.stackBaseSnapshotThroughChangeset(baseCtx, conn, cfg, ws, out.stack, activeBaseID)
	cancel()
	if err != nil {
		if isOfflineError(err) {
			return out, userError("offline", "the server is unreachable, and the edits of the changesets this one builds on are not on this machine yet",
				"Run gs status once while online; after that it works offline.")
		}
		return out, err
	}
	out.base = base
	if out.stack != nil && !out.offline {
		// stackBaseSnapshotThroughChangeset filled in the changesets it
		// read, so the kept tree serves offline runs on its own.
		r.writeCachedDependencyTree(out.stack)
	}
	out.edits, out.current, err = r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, base, false)
	return out, err
}

func (r Runner) dependencyTreeCachePath() (string, error) {
	root, err := r.workspaceRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".gs", "dependency-tree.json"), nil
}

func (r Runner) readCachedDependencyTree(stackID string) *corev1.ChangesetStack {
	path, err := r.dependencyTreeCachePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var stack corev1.ChangesetStack
	if protojson.Unmarshal(data, &stack) != nil || stack.GetId() != stackID {
		return nil
	}
	return &stack
}

// writeCachedDependencyTree keeps the tree for offline runs; failing to
// keep it is not an error.
func (r Runner) writeCachedDependencyTree(stack *corev1.ChangesetStack) {
	path, err := r.dependencyTreeCachePath()
	if err != nil {
		return
	}
	data, err := protojson.Marshal(stack)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (r Runner) runStatus(ctx context.Context, opts commandOptions) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	// Lazily: offline, the calls below fail fast instead of the dial waiting.
	conn, err := dialLazy(cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	found, err := r.loadWorkspaceEdits(ctx, conn, cfg, ws, state)
	if err != nil {
		return err
	}
	stack, edits := found.stack, found.edits
	changedPaths := changedPathsFromEdits(edits)
	if stack == nil && !found.offline && len(edits) > 0 {
		callCtx, cancel := context.WithTimeout(authContext(ctx, cfg), statusServerTimeout)
		validation, err := corev1.NewWorkspaceServiceClient(conn).ValidateWorkspaceDiff(callCtx, &corev1.ValidateWorkspaceDiffRequest{
			Workspace:    workspaceRef(ws),
			BaseCommitId: state.BaseCommitID,
			FileEdits:    edits,
		})
		cancel()
		switch {
		case err == nil:
			changedPaths = validation.AffectedPaths
		case isOfflineError(err):
			found.offline = true
		default:
			return err
		}
	}
	cache, _ := r.objectCache()
	changes := summarizeChanges(edits, found.base, found.current, cache)
	added, removed, _ := changeTotals(changes)
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
		Changes:            changes,
		LinesAdded:         added,
		LinesRemoved:       removed,
		ConflictCount:      len(conflictPaths),
		ConflictPaths:      conflictPaths,
		CurrentChangesetID: state.CurrentChangesetID,
		CurrentPatchsetID:  state.CurrentPatchsetID,
		Offline:            found.offline,
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
		if output.Changes == nil {
			output.Changes = []pathChange{}
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
	if found.offline {
		note := "showing this workspace's own edits"
		if stack != nil {
			note += ", and the dependency tree as last seen"
		}
		fmt.Fprintf(r.Stdout, "offline: %s is unreachable; %s\n", cfg.ServerAddr, note)
	}
	if stack != nil {
		fmt.Fprintf(r.Stdout, "dependencies: %s\n", stack.Id)
		if state.CurrentChangesetID != "" {
			fmt.Fprintf(r.Stdout, "active: %s\n", firstNonEmpty(storage.ShortChangesetID(state.CurrentChangesetID), state.CurrentChangesetID))
		}
		printStack(r.Stdout, stack)
	}
	if len(changes) == 0 && output.ConflictCount == 0 {
		fmt.Fprintln(r.Stdout, "status: clean")
		return nil
	}
	fmt.Fprintf(r.Stdout, "status: %s\n", changeHeadline(changes))
	if output.ConflictCount > 0 {
		fmt.Fprintf(r.Stdout, "conflicts: %d unresolved path(s)\n", output.ConflictCount)
		for _, p := range output.ConflictPaths {
			fmt.Fprintf(r.Stdout, "  %s\n", p)
		}
	}
	printChangeSummary(r.Stdout, changes, "  ", r.colorEnabled(opts))
	return nil
}

// changeHeadline is "3 changed path(s), +12 -4" (the counts only when some
// lines were counted).
func changeHeadline(changes []pathChange) string {
	line := fmt.Sprintf("%d changed path(s)", len(changes))
	added, removed, _ := changeTotals(changes)
	if added > 0 || removed > 0 {
		line += fmt.Sprintf(", +%d -%d", added, removed)
	}
	return line
}
