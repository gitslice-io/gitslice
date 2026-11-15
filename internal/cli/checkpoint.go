package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	"gitslice.io/gitslice/internal/lazyfs"
)

// Checkpoints (design/30_lazy_workspaces.md, "Checkpoints") are a lazy
// workspace's edits saved on this machine: what gs status would report, by
// content hash, with the content in the local object cache. Nothing is
// uploaded. gs takes one before anything replaces the local layer (sync,
// switch, restore, rewind), and gs checkpoint takes one on demand. gs rewind
// puts the workspace back to one; gs fork mounts a new workspace from one.

// maxAutoCheckpoints is how many automatic checkpoints a workspace keeps;
// ones taken with gs checkpoint are kept until deleted with the workspace.
const maxAutoCheckpoints = 50

type checkpointRecord struct {
	Number     int              `json:"number"`
	CreatedAt  string           `json:"created_at"`
	Message    string           `json:"message,omitempty"`
	Auto       bool             `json:"auto,omitempty"`
	BaseCommit string           `json:"base_commit"`
	Edits      []checkpointEdit `json:"edits"`
}

type checkpointEdit struct {
	Op          string `json:"op"` // upsert or delete
	Path        string `json:"path"`
	ContentHash string `json:"content_hash,omitempty"`
	Mode        uint32 `json:"mode,omitempty"`
}

func (r Runner) checkpointDir(id string) string {
	return filepath.Join(r.mountStateDir(id), "checkpoints")
}

func (r Runner) checkpoints(id string) ([]checkpointRecord, error) {
	entries, err := os.ReadDir(r.checkpointDir(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []checkpointRecord
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var cp checkpointRecord
		if err := readJSONFile(filepath.Join(r.checkpointDir(id), entry.Name()), &cp); err != nil {
			continue
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// takeCheckpoint saves the workspace's edits. An automatic checkpoint that
// would repeat the newest one is not taken.
func (r Runner) takeCheckpoint(ctx context.Context, lw *lazyWorkspace, ws WorkspaceConfig, message string, auto bool) (checkpointRecord, bool, error) {
	source := overlayEditSource{fs: lw.layerView(), ws: ws, mountRoot: lw.root}
	reported, err := source.edits(ctx, manifestSnapshot(lw.manifest), 0)
	if err != nil {
		return checkpointRecord{}, false, err
	}
	cache, err := r.objectCache()
	if err != nil {
		return checkpointRecord{}, false, err
	}
	edits := make([]checkpointEdit, 0, len(reported.edits))
	for _, edit := range reported.edits {
		cp := checkpointEdit{Op: edit.GetOp(), Path: edit.GetPath(), ContentHash: edit.GetContentHash(), Mode: edit.GetMode()}
		if cp.Op != "delete" {
			cp.Op = "upsert"
			// The content must outlive the file: keep it in the cache.
			if !cache.Exists(cp.ContentHash) {
				local := filepath.Join(lw.root, filepath.FromSlash(strings.TrimPrefix(cp.Path, "/")))
				if cp.Mode == lazyfs.ModeSymlink {
					target, err := os.Readlink(local)
					if err != nil {
						return checkpointRecord{}, false, err
					}
					_, err = cache.PutBytes([]byte(target))
					if err != nil {
						return checkpointRecord{}, false, err
					}
				} else if _, err := cache.PutFile(local); err != nil {
					return checkpointRecord{}, false, err
				}
			}
		}
		edits = append(edits, cp)
	}
	existing, err := r.checkpoints(lw.id)
	if err != nil {
		return checkpointRecord{}, false, err
	}
	if auto {
		if len(edits) == 0 {
			return checkpointRecord{}, false, nil
		}
		if n := len(existing); n > 0 && existing[n-1].BaseCommit == lw.manifest.CommitID() && sameCheckpointEdits(existing[n-1].Edits, edits) {
			return existing[n-1], false, nil
		}
	}
	number := 1
	if n := len(existing); n > 0 {
		number = existing[n-1].Number + 1
	}
	cp := checkpointRecord{
		Number:     number,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Message:    strings.TrimSpace(message),
		Auto:       auto,
		BaseCommit: lw.manifest.CommitID(),
		Edits:      edits,
	}
	if err := os.MkdirAll(r.checkpointDir(lw.id), 0o700); err != nil {
		return cp, false, err
	}
	if err := writeJSONFile(filepath.Join(r.checkpointDir(lw.id), strconv.Itoa(number)+".json"), cp, 0o600); err != nil {
		return cp, false, err
	}
	r.pruneCheckpoints(lw.id, append(existing, cp))
	return cp, true, nil
}

// autoCheckpoint takes a checkpoint before reason replaces the layer. A
// failure is said, not fatal: it must not stop the command it protects.
func (r Runner) autoCheckpoint(ctx context.Context, lw *lazyWorkspace, reason string) {
	ws, err := r.readWorkspaceConfig()
	if err == nil {
		_, _, err = r.takeCheckpoint(ctx, lw, ws, reason, true)
	}
	if err != nil {
		fmt.Fprintf(r.stderr(), "warning: no checkpoint before %s: %v\n", reason, err)
	}
}

func (r Runner) pruneCheckpoints(id string, all []checkpointRecord) {
	var auto []checkpointRecord
	for _, cp := range all {
		if cp.Auto {
			auto = append(auto, cp)
		}
	}
	for len(auto) > maxAutoCheckpoints {
		_ = os.Remove(filepath.Join(r.checkpointDir(id), strconv.Itoa(auto[0].Number)+".json"))
		auto = auto[1:]
	}
}

func sameCheckpointEdits(a, b []checkpointEdit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkpointSnapshot is the workspace as the checkpoint saw it: base with
// the checkpoint's edits.
func checkpointSnapshot(base *lazyfs.Manifest, cp checkpointRecord) BaseSnapshot {
	snapshot := manifestSnapshot(base)
	for _, edit := range cp.Edits {
		if edit.Op == "delete" {
			delete(snapshot.Files, edit.Path)
			continue
		}
		snapshot.Files[edit.Path] = BaseSnapshotFile{Path: edit.Path, RelPath: strings.TrimPrefix(edit.Path, "/"), ContentHash: edit.ContentHash, Mode: edit.Mode}
	}
	return snapshot
}

func (r Runner) findCheckpoint(id, selector string) (checkpointRecord, error) {
	all, err := r.checkpoints(id)
	if err != nil {
		return checkpointRecord{}, err
	}
	if len(all) == 0 {
		return checkpointRecord{}, userError("no_checkpoints", "this workspace has no checkpoints", "gs checkpoint saves one; gs takes one before sync, switch, restore and rewind.")
	}
	number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(selector), "#"))
	if err != nil {
		return checkpointRecord{}, userError("invalid_checkpoint", "a checkpoint is named by its number: "+selector, "gs checkpoint list shows them.")
	}
	for _, cp := range all {
		if cp.Number == number {
			return cp, nil
		}
	}
	return checkpointRecord{}, userError("checkpoint_not_found", "no checkpoint "+selector+" in this workspace", "gs checkpoint list shows them.")
}

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
		if cp.BaseCommit != lw.manifest.CommitID() {
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
			if cp.BaseCommit == lw.manifest.CommitID() && !sameCheckpointEdits(cp.Edits, current.Edits) {
				target, found = cp, true
				break
			}
		}
		if !found {
			return userError("no_checkpoint_to_rewind_to", "no earlier checkpoint on this base differs from the workspace now", "gs checkpoint list shows them; gs fork <n> <dir> opens one taken on an older base.")
		}
	}
	if target.BaseCommit != lw.manifest.CommitID() {
		return userError("checkpoint_on_older_base", fmt.Sprintf("checkpoint %d was taken on an older base, before the workspace moved (gs sync)", target.Number), fmt.Sprintf("Open it in a new workspace: gs fork %d <dir>.", target.Number))
	}
	cfg, conn, err := r.lazyRemote()
	if err != nil {
		return err
	}
	defer conn.Close()
	// switchLazyWorkspace checkpoints what it replaces.
	if err := r.switchLazyWorkspace(ctx, conn, cfg, ws, lw, nil, checkpointSnapshot(lw.manifest, target)); err != nil {
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
	if err := r.runMount(ctx, quiet, lw.rec.Account+":"+lw.rec.Slice, abs, "", false, false, nil, nil); err != nil {
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
	base := fw.manifest
	if cp.BaseCommit != base.CommitID() {
		if base, err = forked.lazyListingAt(ctx, conn, cfg, fw, cp.BaseCommit); err != nil {
			return err
		}
	}
	ws, err := forked.readWorkspaceConfig()
	if err != nil {
		return err
	}
	if err := forked.switchLazyWorkspace(ctx, conn, cfg, ws, fw, base, checkpointSnapshot(base, cp)); err != nil {
		return err
	}
	if base.CommitID() != lw.manifest.CommitID() || base.CommitID() != fw.rec.BaseCommit {
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
	conn, err := dialLazy(cfg.ServerAddr)
	return cfg, conn, err
}

func shortCommitLabel(commit string) string {
	trimmed := strings.TrimPrefix(commit, "sha256:")
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	return trimmed
}
