package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// workspaceDeleteOutput is what gs workspace delete --json prints.
type workspaceDeleteOutput struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // "lazy" or "checkout"
	Slice   string `json:"slice"`
	Deleted bool   `json:"deleted"`
	// Discarded is how many local edits went with it (only with --force).
	Discarded int `json:"discarded_edits"`
}

// runWorkspaceDelete removes a workspace from this machine: a lazy one is
// unmounted, its state (local edits, checkpoints) deleted and its empty mount
// directory removed; a checkout's directory is removed. It refuses while the
// workspace has local edits no changeset holds, unless force says to discard
// them: what was created or submitted is on the server and is not lost.
func (r Runner) runWorkspaceDelete(ctx context.Context, opts commandOptions, dirArg string, force bool) error {
	dir := strings.TrimSpace(dirArg)
	if dir == "" {
		dir = r.cwd()
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.cwd(), dir)
	}
	dir = filepath.Clean(dir)

	if rec, ok := r.lazyWorkspaceAt(dir); ok {
		return r.deleteLazyWorkspace(opts, rec, force)
	}
	root, err := workspaceRootFrom(dir)
	if err != nil {
		return err
	}
	if root != dir {
		return userError("not_workspace_root", dir+" is inside the workspace at "+root, "Pass the workspace's own directory: gs workspace delete "+root)
	}
	return r.deleteCheckoutWorkspace(ctx, opts, root, force)
}

func (r Runner) deleteLazyWorkspace(opts commandOptions, rec mountRecord, force bool) error {
	mounted := isMounted(rec.MountPath)
	edits, err := r.lazyWorkspaceEdits(rec, mounted)
	if err != nil && !force {
		return userError("edits_unknown", "cannot tell whether "+rec.MountPath+" has local edits: "+err.Error(), "Run gs workspace delete "+rec.MountPath+" --force to delete it anyway, discarding any.")
	}
	if len(edits) > 0 && !force {
		paths := make([]string, len(edits))
		for i, edit := range edits {
			paths[i] = edit.Path
		}
		return localEditsError(rec.MountPath, paths)
	}
	if mounted {
		if err := unmountLazyWorkspace(rec); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(r.mountStateDir(rec.ID)); err != nil {
		return err
	}
	// The mount directory itself, now that nothing is mounted on it: only if
	// it is empty, never what someone put there.
	removedDir := os.Remove(rec.MountPath) == nil || errors.Is(statErr(rec.MountPath), os.ErrNotExist)
	return r.reportWorkspaceDelete(opts, workspaceDeleteOutput{Path: rec.MountPath, Kind: "lazy", Slice: rec.Account + ":" + rec.Slice, Deleted: true, Discarded: len(edits)}, removedDir)
}

// lazyWorkspaceEdits lists a lazy workspace's local edits: from its mount
// daemon while mounted, else from its local layer on disk.
func (r Runner) lazyWorkspaceEdits(rec mountRecord, mounted bool) ([]lazyfs.Edit, error) {
	if mounted {
		reply, err := callMountDaemon(rec.ID, mountControlRequest{Op: "edits"})
		if err == nil && reply.Error == "" {
			return reply.Edits, nil
		}
		if err == nil {
			err = errors.New(reply.Error)
		}
		return nil, err
	}
	local := filepath.Join(r.mountStateDir(rec.ID), "local")
	if recorded, err := lazyfs.RecordedBase(local); err == nil && recorded != "" && recorded != rec.BaseCommit {
		if _, err := os.Stat(r.mountManifestPath(rec.ID, recorded)); err == nil {
			rec.BaseCommit = recorded
		}
	}
	manifest, err := r.loadMountManifest(rec)
	if err != nil {
		return nil, err
	}
	cache, err := r.objectCache()
	if err != nil {
		return nil, err
	}
	layer, err := lazyfs.Open(local, manifest, cacheContent{cache: cache}, lazyfs.Options{})
	if err != nil {
		return nil, err
	}
	defer layer.Close()
	return layer.Edits()
}

func (r Runner) deleteCheckoutWorkspace(ctx context.Context, opts commandOptions, root string, force bool) error {
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == root {
		return userError("refuse_home", "refusing to delete your home directory", "Move the workspace out of your home directory's root first.")
	}
	if filepath.Dir(root) == root {
		return userError("refuse_root", "refusing to delete "+root, "")
	}
	in := r
	in.Dir = root
	ws, err := in.readWorkspaceConfig()
	if err != nil {
		return err
	}
	paths, err := in.checkoutEditPaths(ctx)
	if err != nil && !force {
		return userError("edits_unknown", "cannot tell whether "+root+" has local edits: "+err.Error(), "Run gs workspace delete "+root+" --force to delete it anyway, discarding any.")
	}
	if len(paths) > 0 && !force {
		return localEditsError(root, paths)
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return r.reportWorkspaceDelete(opts, workspaceDeleteOutput{Path: root, Kind: "checkout", Slice: ws.Account + ":" + ws.Slice, Deleted: true, Discarded: len(paths)}, true)
}

// checkoutEditPaths lists the paths a checkout workspace changed that no
// changeset holds: its edits against its base, less those that a changeset
// of its dependency tree has, the same, in its current patchset (created,
// submitted or landed, they are on the server). An edit a changeset holds
// differently, or only as part of a rename or copy, counts as not held.
func (r Runner) checkoutEditPaths(ctx context.Context) ([]string, error) {
	cfg, ws, state, err := r.loadLocalState()
	if err != nil {
		return nil, err
	}
	conn, err := dialLazy(cfg.ServerAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	base, err := r.readBaseSnapshot()
	if err != nil {
		return nil, err
	}
	edits, _, err := r.snapshotEditsAgainstBase(ctx, conn, cfg, ws, base, false)
	if err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return nil, nil
	}
	held, err := r.heldEdits(ctx, conn, cfg, state)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, edit := range edits {
		if !held[editKey(edit)] {
			paths = append(paths, edit.Path)
		}
	}
	return paths, nil
}

// heldEdits is every upsert and delete in the current patchset of a
// changeset of the workspace's dependency tree.
func (r Runner) heldEdits(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, state WorkspaceState) (map[string]bool, error) {
	held := map[string]bool{}
	if state.ActiveStackID == "" {
		return held, nil
	}
	callCtx, cancel := context.WithTimeout(authContext(ctx, cfg), statusServerTimeout)
	defer cancel()
	stack, err := corev1.NewChangesetStackServiceClient(conn).GetStack(callCtx, &corev1.GetStackRequest{StackId: state.ActiveStackID})
	if err != nil {
		if !isOfflineError(err) {
			return nil, err
		}
		if stack = r.readCachedDependencyTree(state.ActiveStackID); stack == nil {
			return nil, err
		}
	}
	changesets := corev1.NewChangesetServiceClient(conn)
	for _, entry := range stack.GetEntries() {
		cs := entry.GetChangeset()
		if cs == nil && entry.GetChangesetId() != "" {
			if cs, err = changesets.GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: entry.GetChangesetId()}); err != nil {
				return nil, err
			}
		}
		patchset := currentPatchset(cs)
		if patchset == nil {
			continue
		}
		for _, edit := range patchset.GetFileEdits() {
			if edit.GetOp() == "delete" || edit.GetOp() == "upsert" || edit.GetOp() == "" {
				held[editKey(edit)] = true
			}
		}
	}
	return held, nil
}

func editKey(edit *corev1.FileEdit) string {
	op := edit.GetOp()
	if op == "" {
		op = "upsert"
	}
	if op == "delete" {
		return "delete\x00" + edit.GetPath()
	}
	return op + "\x00" + edit.GetPath() + "\x00" + edit.GetContentHash()
}

func localEditsError(dir string, paths []string) error {
	shown := paths
	if len(shown) > 5 {
		shown = shown[:5]
	}
	more := ""
	if len(paths) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(paths)-len(shown))
	}
	noun := "edit"
	if len(paths) != 1 {
		noun = "edits"
	}
	return userError("local_edits",
		fmt.Sprintf("%s has %d local %s that no changeset holds: %s%s", dir, len(paths), noun, strings.Join(shown, ", "), more),
		"Run gs create to keep them (or gs checkpoint, in a lazy workspace), or gs workspace delete "+dir+" --force to discard them.")
}

func (r Runner) reportWorkspaceDelete(opts commandOptions, out workspaceDeleteOutput, removedDir bool) error {
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "deleted %s workspace %s (%s)", out.Kind, out.Path, out.Slice)
	if out.Discarded > 0 {
		fmt.Fprintf(r.Stdout, ", discarding %d local edit(s)", out.Discarded)
	}
	fmt.Fprintln(r.Stdout)
	if !removedDir {
		fmt.Fprintf(r.Stdout, "%s is not empty, so it was left in place\n", out.Path)
	}
	if cwd := r.cwd(); cwd == out.Path || strings.HasPrefix(cwd, out.Path+string(filepath.Separator)) {
		fmt.Fprintln(r.Stdout, "your shell was in it: cd somewhere else")
	}
	return nil
}

func statErr(path string) error {
	_, err := os.Stat(path)
	return err
}
