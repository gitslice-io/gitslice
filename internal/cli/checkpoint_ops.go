package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"google.golang.org/grpc"

	"gitslice.io/gitslice/internal/lazyfs"
)

func (r Runner) requireLazyWorkspace(command string) (*lazyWorkspace, WorkspaceConfig, error) {
	lw, err := r.currentLazyWorkspace()
	if err != nil {
		return nil, WorkspaceConfig{}, err
	}
	if lw == nil {
		return nil, WorkspaceConfig{}, userError("checkpoints_unsupported", command+" works in lazy workspaces (gs init --lazy, gs mount) for now", "Mount the slice with gs init --lazy <slice> <dir>.")
	}
	ws, err := r.readWorkspaceConfig()
	return lw, ws, err
}

func (r Runner) runCheckpoint(ctx context.Context, opts commandOptions, message string) error {
	lw, ws, err := r.requireLazyWorkspace("gs checkpoint")
	if err != nil {
		return err
	}
	cp, _, err := r.takeCheckpoint(ctx, lw, ws, message, false)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, checkpointSummary(cp))
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "checkpoint %d: %s; gs rewind %d puts the workspace back to it\n", cp.Number, editCountText(len(cp.Edits)), cp.Number)
	}
	return nil
}

func (r Runner) runCheckpointList(opts commandOptions) error {
	lw, _, err := r.requireLazyWorkspace("gs checkpoint list")
	if err != nil {
		return err
	}
	all, err := r.checkpoints(lw.id)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		out := make([]map[string]any, 0, len(all))
		for _, cp := range all {
			out = append(out, checkpointSummary(cp))
		}
		return r.writeJSONOutput(opts, map[string]any{"checkpoints": out})
	}
	if len(all) == 0 {
		fmt.Fprintln(r.Stdout, "no checkpoints yet; gs checkpoint saves one")
		return nil
	}
	for i := len(all) - 1; i >= 0; i-- {
		cp := all[i]
		label := cp.Message
		if label == "" {
			label = "(no message)"
		}
		base := ""
		if cp.BaseCommit != lw.baseCommit() {
			base = " (older base; gs fork it)"
		}
		fmt.Fprintf(r.Stdout, "%d\t%s\t%s\t%s%s\n", cp.Number, cp.CreatedAt, editCountText(len(cp.Edits)), label, base)
	}
	return nil
}

func checkpointSummary(cp checkpointRecord) map[string]any {
	return map[string]any{"number": cp.Number, "created_at": cp.CreatedAt, "message": cp.Message, "auto": cp.Auto, "base_commit": cp.BaseCommit, "edits": len(cp.Edits)}
}

func editCountText(n int) string {
	if n == 1 {
		return "1 edit"
	}
	return fmt.Sprintf("%d edits", n)
}

// runRewind puts the workspace back to a checkpoint: the newest one that
// differs from now, unless one is named. What it replaces is checkpointed
// first, so a rewind can itself be undone.
func (r Runner) runRewind(ctx context.Context, opts commandOptions, selector string) error {
	lw, ws, err := r.requireLazyWorkspace("gs rewind")
	if err != nil {
		return err
	}
	var target checkpointRecord
	if strings.TrimSpace(selector) != "" {
		if target, err = r.findCheckpoint(lw.id, selector); err != nil {
			return err
		}
	} else {
		current, _, err := r.takeCheckpoint(ctx, lw, ws, "before gs rewind", true)
		if err != nil {
			return err
		}
		all, err := r.checkpoints(lw.id)
		if err != nil {
			return err
		}
		found := false
		for i := len(all) - 1; i >= 0; i-- {
			cp := all[i]
			if cp.BaseCommit == lw.baseCommit() && !sameCheckpointEdits(cp.Edits, current.Edits) {
				target, found = cp, true
				break
			}
		}
		if !found {
			return userError("no_checkpoint_to_rewind_to", "no earlier checkpoint on this base differs from the workspace now", "gs checkpoint list shows them; gs fork <n> <dir> opens one taken on an older base.")
		}
	}
	if target.BaseCommit != lw.baseCommit() {
		return userError("checkpoint_on_older_base", fmt.Sprintf("checkpoint %d was taken on an older base, before the workspace moved (gs sync)", target.Number), fmt.Sprintf("Open it in a new workspace: gs fork %d <dir>.", target.Number))
	}
	baseManifest, err := lw.baseManifest()
	if err != nil {
		return err
	}
	snap, err := checkpointSnapshot(baseManifest, target)
	if err != nil {
		return err
	}
	cfg, conn, err := r.lazyRemote()
	if err != nil {
		return err
	}
	defer conn.Close()
	// switchLazyWorkspace checkpoints what it replaces.
	if err := r.switchLazyWorkspace(ctx, conn, cfg, ws, lw, nil, snap); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, checkpointSummary(target))
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "rewound to checkpoint %d (%s); what was here is a checkpoint too, gs checkpoint list shows it\n", target.Number, editCountText(len(target.Edits)))
	}
	return nil
}

// runFork mounts a new lazy workspace at dir showing a checkpoint, on the
// base it was taken on.
func (r Runner) runFork(ctx context.Context, opts commandOptions, selector, dir string) error {
	lw, _, err := r.requireLazyWorkspace("gs fork")
	if err != nil {
		return err
	}
	cp, err := r.findCheckpoint(lw.id, selector)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(filepath.Join(r.cwd(), dir))
	if filepath.IsAbs(dir) {
		abs, err = filepath.Abs(dir)
	}
	if err != nil {
		return err
	}
	quiet := opts
	quiet.Quiet = true
	if err := r.runMount(ctx, quiet, lw.rec.Account+":"+lw.rec.Slice, abs, "", false, false, nil, nil, ""); err != nil {
		return err
	}
	forked := r
	forked.Dir = abs
	fw, err := forked.currentLazyWorkspace()
	if err != nil || fw == nil {
		return fmt.Errorf("the fork mounted at %s but gs cannot open it: %v", abs, err)
	}
	cfg, conn, err := forked.lazyRemote()
	if err != nil {
		return err
	}
	defer conn.Close()
	var base *lazyfs.Manifest
	if cp.BaseCommit == fw.baseCommit() {
		if base, err = fw.baseManifest(); err != nil {
			return err
		}
	} else {
		if base, err = forked.lazyListingAt(ctx, conn, cfg, fw, cp.BaseCommit); err != nil {
			return err
		}
	}
	ws, err := forked.readWorkspaceConfig()
	if err != nil {
		return err
	}
	snap, err := checkpointSnapshot(base, cp)
	if err != nil {
		return err
	}
	if err := forked.switchLazyWorkspace(ctx, conn, cfg, ws, fw, base, snap); err != nil {
		return err
	}
	if base.CommitID() != lw.baseCommit() || base.CommitID() != fw.rec.BaseCommit {
		if err := forked.writeLazyWorkspaceState(fw.rec, base); err != nil {
			return err
		}
	}
	if opts.jsonOutput() {
		out := checkpointSummary(cp)
		out["mount_path"] = abs
		return r.writeJSONOutput(opts, out)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "forked checkpoint %d into a new lazy workspace at %s (%s on base %s); gs unmount %s to stop\n", cp.Number, abs, editCountText(len(cp.Edits)), shortCommitLabel(cp.BaseCommit), abs)
	}
	return nil
}

// lazyRemote is the connection a lazy workspace command uses to read what the
// cache lacks: made without waiting, so a rewind works offline when the cache
// has everything.
func (r Runner) lazyRemote() (UserConfig, *grpc.ClientConn, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return cfg, nil, err
	}
	conn, err := r.dialServerLazy(context.Background(), cfg.ServerAddr)
	return cfg, conn, err
}

func shortCommitLabel(commit string) string {
	trimmed := strings.TrimPrefix(commit, "sha256:")
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	return trimmed
}
