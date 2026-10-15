package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
)

func (r Runner) runWorkspaceInit(ctx context.Context, opts commandOptions, sliceRef string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	if err := r.requireEmptyWorkspaceInitDir(); err != nil {
		return err
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	// One streamed call gives the slice, the head, the listing and, into an
	// empty cache, the files.
	checkout, err := receiveCheckout(callCtx, conn, ref, cache)
	if err != nil {
		return err
	}
	slice, commitID := checkout.slice, checkout.commitID
	workspace := WorkspaceConfig{
		Account:        ref.Account,
		Slice:          ref.Slice,
		SliceID:        slice.Id,
		DefinitionHash: slice.DefinitionHash,
		IncludedPaths:  slice.Definition.IncludedPaths,
		BaseCommitID:   commitID,
		ConversationID: strings.TrimSpace(opts.AgentConversationID),
	}
	if err := r.writeWorkspaceConfig(workspace); err != nil {
		return err
	}
	if err := r.writeWorkspaceState(WorkspaceState{BaseCommitID: commitID}); err != nil {
		return err
	}
	if err := r.writeBaseSnapshot(BaseSnapshot{CommitID: commitID, Files: map[string]BaseSnapshotFile{}}); err != nil {
		return err
	}
	hydrated, err := r.hydrateListing(callCtx, conn, workspace, checkout, cache)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"workspace":           ref.Account + ":" + ref.Slice,
			"slice_id":            slice.Id,
			"base_commit_id":      commitID,
			"client_object_cache": cache.Root(),
			"hydrated":            hydrated,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "initialized workspace for %s:%s\n", ref.Account, ref.Slice)
	fmt.Fprintf(r.Stdout, "hydrated %d file(s) through cache (%d hit(s), %d miss(es))\n", hydrated.FileCount, hydrated.CacheHits, hydrated.CacheMisses)
	return nil
}

func (r Runner) requireEmptyWorkspaceInitDir() error {
	if root, err := r.workspaceRoot(); err == nil {
		return userError("already_in_workspace", "already inside a gitslice workspace: "+root, "Create a new empty directory outside the existing workspace.")
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return err
	}
	entries, err := os.ReadDir(r.cwd())
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	return userError("workspace_not_empty", "workspace init requires an empty directory", "Create a new empty directory and run gs init <slice|account:slice> there.")
}

// requireAgentWorkspaceDir validates the working directory for `gs agent start`.
// Unlike requireEmptyWorkspaceInitDir (used by `gs init`), it tolerates the
// daemon's own prior state — the `conversations/` subdir written by an earlier
// run — so the daemon can restart in place and rehydrate those conversations
// (and their runtime thread continuity) from disk. It still refuses to run
// inside an existing gitslice workspace, and refuses a directory holding
// unrelated files so the daemon is not pointed at a populated dir by mistake.
func (r Runner) requireAgentWorkspaceDir() error {
	if root, err := r.workspaceRoot(); err == nil {
		return userError("already_in_workspace", "already inside a gitslice workspace: "+root, "Start the agent daemon in its own directory, outside any workspace.")
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return err
	}
	entries, err := os.ReadDir(r.cwd())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		// The daemon's own conversations live under conversations/; ignore
		// hidden dotfiles (e.g. .DS_Store) that tools may drop. Anything else
		// means this is not a daemon-owned directory.
		if name == "conversations" || strings.HasPrefix(name, ".") {
			continue
		}
		return userError("agent_workspace_not_empty", "agent workspace directory has unexpected contents: "+name, "Start the agent daemon in an empty directory (a prior daemon's conversations/ is allowed).")
	}
	return nil
}

func (r Runner) runWorkspaceHydrate(ctx context.Context, opts commandOptions, requested []string) error {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return err
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	baseCommitID := state.BaseCommitID
	if baseCommitID == "" {
		baseCommitID = ws.BaseCommitID
	}
	if baseCommitID == "" {
		return userError("invalid_workspace_state", "workspace has no base commit", "Run gs init <slice|account:slice> again.")
	}
	hydrated, err := r.hydrateWorkspacePaths(authContext(ctx, cfg), conn, ws, baseCommitID, requested, cache)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"workspace":           ws.Account + ":" + ws.Slice,
			"base_commit_id":      baseCommitID,
			"client_object_cache": cache.Root(),
			"hydrated":            hydrated,
		})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "hydrated %d file(s) through cache (%d hit(s), %d miss(es))\n", hydrated.FileCount, hydrated.CacheHits, hydrated.CacheMisses)
	return nil
}
