package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (r Runner) runMount(ctx context.Context, opts commandOptions, sliceArg, dirArg, workspaceID string, foreground, debug bool, readOnly *bool, prefetch []string) (err error) {
	for _, pattern := range prefetch {
		if !doublestar.ValidatePattern(pattern) {
			return userError("invalid_args", "invalid --prefetch pattern "+pattern, "Use a glob relative to the mount, such as 'acme/app/**/*.go'.")
		}
	}
	dir, err := filepath.Abs(dirArg)
	if err != nil {
		return err
	}
	// Refuse a directory that cannot be mounted before anything is created, so
	// a refusal leaves no workspace behind.
	if !isMounted(dir) {
		if err := checkMountDir(dir); err != nil {
			return err
		}
	}
	rec, created, err := r.mountRecordFor(ctx, sliceArg, dir, workspaceID, strings.TrimSpace(opts.AgentConversationID))
	if err != nil {
		return err
	}
	if created {
		// A workspace this call made has no edits: if it cannot be mounted, it
		// is not worth keeping in gs mount list.
		defer func() {
			if err != nil {
				_ = os.RemoveAll(r.mountStateDir(rec.ID))
			}
		}()
	}
	if isMounted(dir) {
		if _, err := callMountDaemon(rec.ID, mountControlRequest{Op: "status"}); err == nil || foreground {
			return userError("already_mounted", dir+" is already a mount point", "Run gs mount list, or gs unmount "+dir+" first.")
		}
		// The daemon is gone and left its mount behind ("transport endpoint
		// is not connected"): clear it and serve the workspace again. Its
		// local layer is on disk, edits included.
		if err := forceUnmount(dir); err != nil {
			return userError("stale_mount", "a dead mount is left at "+dir+": "+err.Error(), "Run fusermount3 -u "+dir+" and try again.")
		}
	}
	if readOnly != nil && *readOnly != rec.ReadOnly {
		rec.ReadOnly = *readOnly
		if err := r.writeMountRecord(rec); err != nil {
			return err
		}
	}
	if prefetch != nil {
		rec.Prefetch = prefetch
		if err := r.writeMountRecord(rec); err != nil {
			return err
		}
	}
	if err := prepareMountDir(dir); err != nil {
		return err
	}
	if foreground {
		return r.serveMount(ctx, rec, debug, func() {
			if !opts.Quiet && !opts.jsonOutput() {
				fmt.Fprintf(r.stderr(), "mounted %s:%s at %s; serving until gs unmount\n", rec.Account, rec.Slice, dir)
			}
		})
	}
	if err := r.attachViaSupervisor(rec); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"id": rec.ID, "ref": rec.Account + ":" + rec.Slice, "mount_path": rec.MountPath, "base_commit": rec.BaseCommit, "files": rec.Files})
	}
	if !opts.Quiet {
		mode := "edits stay on this machine until gs create"
		if rec.ReadOnly {
			mode = "read-only"
		}
		fmt.Fprintf(r.Stdout, "mounted %s:%s at %s (%d files, fetched on first read; %s)\n", rec.Account, rec.Slice, rec.MountPath, rec.Files, mode)
		fmt.Fprintf(r.Stdout, "workspace %s; gs unmount %s to stop\n", rec.ID, rec.MountPath)
	}
	return nil
}

// mountRecordFor returns the workspace to mount at dir: the one named, the one
// last mounted at dir, or a new one for the slice.
func (r Runner) mountRecordFor(ctx context.Context, sliceArg, dir, workspaceID, conversationID string) (rec mountRecord, created bool, err error) {
	if workspaceID != "" {
		found, err := r.readMountRecord(workspaceID)
		if err != nil {
			return rec, false, userError("not_a_lazy_workspace", "no lazy workspace "+workspaceID, "Run gs mount list.")
		}
		found.MountPath = dir
		return found, false, r.writeMountRecord(found)
	}
	records, err := r.mountRecords()
	if err != nil {
		return rec, false, err
	}
	for _, existing := range records {
		if existing.MountPath == dir {
			if sliceArg != "" && !strings.EqualFold(strings.ReplaceAll(sliceArg, "/", ":"), existing.Account+":"+existing.Slice) && !strings.EqualFold(sliceArg, existing.Slice) {
				return rec, false, userError("mount_path_in_use", dir+" belongs to the lazy workspace of "+existing.Account+":"+existing.Slice, "Use another directory, or gs mount "+existing.Account+":"+existing.Slice+" "+dir+" to re-attach it.")
			}
			return existing, false, nil
		}
	}
	if sliceArg == "" {
		return rec, false, userError("slice_required", "no lazy workspace at "+dir, "Run gs mount <slice|account:slice> "+dir+".")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return rec, false, err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceArg)
	if err != nil {
		return rec, false, err
	}
	cache, err := r.objectCache()
	if err != nil {
		return rec, false, err
	}
	var checkout *receivedCheckout
	if err = retryUnavailable(callCtx, listingRetryBackoff, func() (err error) {
		checkout, err = receiveCheckoutListing(callCtx, conn, ref, cache)
		return err
	}); err != nil {
		return rec, false, err
	}
	entries := manifestEntries(checkout.entries)
	id, err := newMountID()
	if err != nil {
		return rec, false, err
	}
	state := r.mountStateDir(id)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return rec, false, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(state)
		}
	}()
	if err := writeManifest(r.mountManifestPath(id, checkout.commitID), entries); err != nil {
		return rec, false, err
	}
	files := 0
	for _, entry := range entries {
		if entry.Kind != lazyfs.KindDir {
			files++
		}
	}
	rec = mountRecord{
		ID:             id,
		Account:        checkout.slice.GetRef().GetAccount(),
		Slice:          checkout.slice.GetRef().GetSlice(),
		MountPath:      dir,
		ServerAddr:     cfg.ServerAddr,
		BaseCommit:     checkout.commitID,
		Files:          files,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		SliceID:        checkout.slice.GetId(),
		DefinitionHash: checkout.slice.GetDefinitionHash(),
		IncludedPaths:  checkout.slice.GetDefinition().GetIncludedPaths(),
		ConversationID: conversationID,
	}
	if err = r.writeMountRecord(rec); err != nil {
		return rec, false, err
	}
	manifest, err := r.loadMountManifest(rec)
	if err != nil {
		return rec, false, err
	}
	if err = r.writeLazyWorkspaceState(rec, manifest); err != nil {
		return rec, false, err
	}
	return rec, true, nil
}

// listingRetryBackoff paces the retries of the listing a new mount fetches.
var listingRetryBackoff = []time.Duration{250 * time.Millisecond, 750 * time.Millisecond, 2 * time.Second}

// checkMountDir checks that dir can be mounted before anything is made for it:
// it is not inside a workspace (a gs init checkout or another mount, whose .gs
// the new mount would hide from every command run in it), and it either does
// not exist or is an empty directory.
func checkMountDir(dir string) error {
	if root, err := workspaceRootFrom(filepath.Dir(dir)); err == nil {
		return userError("already_in_workspace", "cannot mount inside the gitslice workspace at "+root, "Mount at a directory outside any workspace.")
	} else if !isUserErrorCode(err, "not_in_workspace") {
		return err
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return userError("mount_dir_unusable", "cannot use "+dir+": "+err.Error(), "Mount at an empty directory.")
	case len(entries) > 0:
		return userError("mount_dir_not_empty", dir+" is not empty", "Mount at an empty directory.")
	}
	return nil
}

// prepareMountDir makes dir, or checks that it is an empty directory.
func prepareMountDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return userError("mount_dir_not_empty", dir+" is not empty", "Mount at an empty directory.")
	}
	return nil
}

func (r Runner) runUnmount(ctx context.Context, opts commandOptions, selector string) error {
	rec, err := r.findMountRecord(selector)
	if err != nil {
		return err
	}
	if !isMounted(rec.MountPath) {
		return userError("not_mounted", rec.MountPath+" is not mounted", "Its workspace is kept; gs mount list shows it.")
	}
	if err := unmountLazyWorkspace(rec); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"id": rec.ID, "mount_path": rec.MountPath, "unmounted": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "unmounted %s; the workspace is kept (gs mount %s:%s %s re-attaches it)\n", rec.MountPath, rec.Account, rec.Slice, rec.MountPath)
	}
	return nil
}

// unmountLazyWorkspace asks rec's daemon to unmount, or unmounts directly
// when it is gone, and waits until the mount is.
func unmountLazyWorkspace(rec mountRecord) error {
	if _, err := callMountDaemon(rec.ID, mountControlRequest{Op: "unmount"}); err != nil {
		if err := forceUnmount(rec.MountPath); err != nil {
			return err
		}
	}
	for i := 0; i < 100 && isMounted(rec.MountPath); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if isMounted(rec.MountPath) {
		return userError("unmount_failed", rec.MountPath+" is still mounted", "A program may be using it (lsof +D "+rec.MountPath+"); close it and try again.")
	}
	return nil
}

// lazyWorkspaceAt returns the lazy workspace last mounted at dir.
func (r Runner) lazyWorkspaceAt(dir string) (mountRecord, bool) {
	records, err := r.mountRecords()
	if err != nil {
		return mountRecord{}, false
	}
	for _, rec := range records {
		if rec.MountPath == dir {
			return rec, true
		}
	}
	return mountRecord{}, false
}

type mountListEntry struct {
	ID         string `json:"id"`
	Ref        string `json:"ref"`
	MountPath  string `json:"mount_path"`
	Mounted    bool   `json:"mounted"`
	BaseCommit string `json:"base_commit"`
	Files      int    `json:"files"`
}

func (r Runner) runMountList(opts commandOptions) error {
	records, err := r.mountRecords()
	if err != nil {
		return err
	}
	out := make([]mountListEntry, 0, len(records))
	for _, rec := range records {
		out = append(out, mountListEntry{ID: rec.ID, Ref: rec.Account + ":" + rec.Slice, MountPath: rec.MountPath, Mounted: isMounted(rec.MountPath), BaseCommit: rec.BaseCommit, Files: rec.Files})
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"workspaces": out})
	}
	if opts.Quiet {
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintln(r.Stdout, "no lazy workspaces; gs mount <slice> <dir> makes one")
		return nil
	}
	for _, entry := range out {
		state := "not mounted"
		if entry.Mounted {
			state = "mounted"
		}
		fmt.Fprintf(r.Stdout, "%s  %s  %s  (%s, %d files, commit %s)\n", entry.ID, entry.Ref, entry.MountPath, state, entry.Files, shortCommit(entry.BaseCommit))
	}
	return nil
}

func shortCommit(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (r Runner) runMountStatus(ctx context.Context, opts commandOptions, selector string) error {
	rec, err := r.findMountRecord(selector)
	if err != nil {
		return err
	}
	mounted := isMounted(rec.MountPath)
	var status *mountControlReply
	localEdits := -1
	if mounted {
		if got, err := callMountDaemon(rec.ID, mountControlRequest{Op: "status"}); err == nil {
			status = &got
		}
		if got, err := callMountDaemon(rec.ID, mountControlRequest{Op: "edits"}); err == nil {
			localEdits = len(got.Edits)
		}
	}
	if opts.jsonOutput() {
		payload := map[string]any{"id": rec.ID, "ref": rec.Account + ":" + rec.Slice, "mount_path": rec.MountPath, "mounted": mounted, "read_only": rec.ReadOnly, "base_commit": rec.BaseCommit, "files": rec.Files, "prefetch": rec.Prefetch, "daemon": status}
		if localEdits >= 0 {
			payload["local_edits"] = localEdits
		}
		return r.writeJSONOutput(opts, payload)
	}
	if opts.Quiet {
		return nil
	}
	state := "not mounted"
	switch {
	case mounted && rec.ReadOnly:
		state = "mounted read-only"
	case mounted:
		state = "mounted"
	}
	fmt.Fprintf(r.Stdout, "%s:%s at %s: %s\n", rec.Account, rec.Slice, rec.MountPath, state)
	fmt.Fprintf(r.Stdout, "workspace %s, base commit %s, %d files\n", rec.ID, shortCommit(rec.BaseCommit), rec.Files)
	if localEdits >= 0 {
		fmt.Fprintf(r.Stdout, "%d local edit(s), on this machine only until gs create (gs status lists them)\n", localEdits)
	}
	if len(rec.Prefetch) > 0 {
		fmt.Fprintf(r.Stdout, "prefetch: %s\n", strings.Join(rec.Prefetch, " "))
	}
	if status != nil {
		fmt.Fprintf(r.Stdout, "opens %d, of which %d fetched; %d files (%s) fetched in all\n", status.Opens, status.Fetches, status.FetchedFiles, humanBytes(status.FetchedBytes))
		if status.Errors > 0 {
			fmt.Fprintf(r.Stdout, "errors %d, last: %s\n", status.Errors, status.LastError)
		}
	}
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

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
	conn, err := dial(ctx, rec.ServerAddr)
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
			if err != nil || strings.HasPrefix(rel, "..") || rel == ".gs" || strings.HasPrefix(rel, ".gs"+string(filepath.Separator)) {
				return userError("outside_workspace", arg+" is not a path of this workspace", "Name paths inside "+root+".")
			}
			if rel == "." {
				req.Paths = append(req.Paths, "/")
			} else {
				req.Paths = append(req.Paths, "/"+filepath.ToSlash(rel))
			}
		}
	}
	reply, err := callMountDaemon(id, req)
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
		case 1:
			fmt.Fprintf(r.Stdout, "restored 1 edit%s from the base\n", scope)
		default:
			fmt.Fprintf(r.Stdout, "restored %d edits%s from the base\n", reply.Changed, scope)
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
	if err := r.runMount(ctx, opts, sliceArg, dirArg, "", false, false, readOnly, prefetch); err != nil {
		return err
	}
	if atCurrent && !opts.jsonOutput() && !opts.Quiet {
		// The shell that ran this is still in the directory underneath the mount.
		fmt.Fprintf(r.Stdout, "your shell is still in the directory under the mount: run  cd \"$PWD\"  to see it\n")
	}
	return nil
}
