package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) runMountFetch(ctx context.Context, opts commandOptions, workspace string, args []string) error {
	selector := workspace
	if selector == "" {
		selector = r.cwd()
	}
	rec, err := r.findMountRecord(selector)
	if err != nil {
		return err
	}
	manifest, err := r.loadMountManifest(rec)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{rec.MountPath}
	}
	var prefixes []string
	for _, arg := range args {
		abs := arg
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(r.cwd(), arg)
		}
		rel, err := filepath.Rel(rec.MountPath, filepath.Clean(abs))
		if err != nil || strings.HasPrefix(rel, "..") {
			return userError("outside_workspace", arg+" is outside the lazy workspace at "+rec.MountPath, "Name paths inside it.")
		}
		prefixes = append(prefixes, "/"+filepath.ToSlash(strings.TrimPrefix(rel, ".")))
	}
	var wanted []lazyfs.BaseEntry
	manifest.Files(func(entry lazyfs.BaseEntry) {
		if entry.Kind != lazyfs.KindFile {
			return
		}
		for _, prefix := range prefixes {
			if prefix == "/" || entry.Path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(entry.Path, strings.TrimSuffix(prefix, "/")+"/") {
				wanted = append(wanted, entry)
				return
			}
		}
	})
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	cached := 0
	var missing []lazyfs.BaseEntry
	seen := map[string]bool{}
	for _, entry := range wanted {
		if seen[entry.ContentHash] || cache.Exists(entry.ContentHash) {
			cached++
			continue
		}
		seen[entry.ContentHash] = true
		missing = append(missing, entry)
	}
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := r.dialServer(ctx, rec.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	fetched, bytes, err := fetchConcurrently(authContext(ctx, cfg), conn, &corev1.SliceRef{Account: rec.Account, Slice: rec.Slice}, rec.BaseCommit, missing, cache)
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"files": len(wanted), "fetched": fetched, "fetched_bytes": bytes, "already_cached": cached})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "fetched %d files (%s); %d were already here\n", fetched, humanBytes(bytes), cached)
	}
	return nil
}

// runRestore drops local edits in a lazy workspace, so the base shows again.
func (r Runner) runRestore(ctx context.Context, opts commandOptions, args []string, all bool) error {
	if all == (len(args) > 0) {
		return userError("invalid_args", "name paths to restore, or pass --all", "Usage: gs restore <path>... | gs restore --all")
	}
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	id := lazyWorkspaceID(root)
	if id == "" {
		return userError("restore_unsupported", "gs restore works in lazy workspaces (gs mount) for now", "In a gs init workspace, copy the file back from gs show or check out again.")
	}
	req := mountControlRequest{Op: "restore", Paths: []string{"/"}}
	if !all {
		req.Paths = nil
		for _, arg := range args {
			abs := arg
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(r.cwd(), arg)
			}
			rel, err := filepath.Rel(root, filepath.Clean(abs))
			if err != nil || strings.HasPrefix(rel, "..") || rel == ".gs" || strings.HasPrefix(rel, ".gs"+string(filepath.Separator)) || (rel != "." && ensureNotReservedGsPath("/"+filepath.ToSlash(rel)) != nil) {
				return userError("outside_workspace", arg+" is not a path of this workspace", "Name paths inside "+root+".")
			}
			if rel == "." {
				req.Paths = append(req.Paths, "/")
			} else {
				req.Paths = append(req.Paths, "/"+filepath.ToSlash(rel))
			}
		}
	}
	if lw, err := r.currentLazyWorkspace(); err == nil && lw != nil {
		r.autoCheckpoint(ctx, lw, "before gs restore")
	}
	reply, err := r.callMountDaemon(id, req)
	if err != nil {
		return userError("restore_failed", err.Error(), "Is the workspace mounted? gs mount status "+root)
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"restored": req.Paths, "dropped": reply.Changed})
	}
	if !opts.Quiet {
		scope := ""
		if !all {
			scope = " under " + strings.Join(args, ", ")
		}
		switch reply.Changed {
		case 0:
			fmt.Fprintf(r.Stdout, "nothing to restore: no local edits%s\n", scope)
		// Undoing an edit puts a changed or deleted file back as the base
		// has it, and removes a file the base never had.
		case 1:
			fmt.Fprintf(r.Stdout, "undid 1 local edit%s; it matches the base again\n", scope)
		default:
			fmt.Fprintf(r.Stdout, "undid %d local edits%s; they match the base again\n", reply.Changed, scope)
		}
	}
	return nil
}

// runMountDiscard deletes an unmounted lazy workspace and its local edits.
func (r Runner) runMountDiscard(ctx context.Context, opts commandOptions, selector string, yes bool) error {
	rec, err := r.findMountRecord(selector)
	if err != nil {
		return err
	}
	if isMounted(rec.MountPath) {
		return userError("still_mounted", rec.MountPath+" is mounted", "Run gs unmount "+rec.MountPath+" first.")
	}
	if !yes {
		return userError("confirmation_required", "discarding deletes the workspace and every local edit in it, which no changeset holds", "Run gs mount discard "+selector+" --yes to confirm.")
	}
	if err := os.RemoveAll(r.mountStateDir(rec.ID)); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"id": rec.ID, "discarded": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "discarded lazy workspace %s (%s:%s at %s)\n", rec.ID, rec.Account, rec.Slice, rec.MountPath)
	}
	return nil
}

// runInitLazy is gs init --lazy: it creates a lazy workspace for the slice and
// mounts it at dir, or at the current directory when none is given.
func (r Runner) runInitLazy(ctx context.Context, opts commandOptions, sliceArg, dirArg string, readOnly *bool, prefetch []string) error {
	atCurrent := strings.TrimSpace(dirArg) == ""
	if atCurrent {
		dirArg = r.cwd()
	}
	if err := r.runMount(ctx, opts, sliceArg, dirArg, "", false, false, readOnly, prefetch, ""); err != nil {
		return err
	}
	if atCurrent && !opts.jsonOutput() && !opts.Quiet {
		// The shell that ran this is still in the directory underneath the mount.
		fmt.Fprintf(r.Stdout, "your shell is still in the directory under the mount: run  cd \"$PWD\"  to see it\n")
	}
	return nil
}
