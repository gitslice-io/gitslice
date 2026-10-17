package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// mountControlRequest and mountControlStatus are the daemon's control protocol:
// one JSON request and one JSON reply per connection.
type mountControlRequest struct {
	Op string `json:"op"` // "status" or "unmount"
}

type mountControlStatus struct {
	PID          int    `json:"pid"`
	MountedAt    string `json:"mounted_at"`
	Opens        int64  `json:"opens"`
	Fetches      int64  `json:"fetches"`
	FetchedFiles int64  `json:"fetched_files"`
	FetchedBytes int64  `json:"fetched_bytes"`
	Errors       int64  `json:"errors"`
	LastError    string `json:"last_error,omitempty"`
}

func (r Runner) runMount(ctx context.Context, opts commandOptions, sliceArg, dirArg, workspaceID string, foreground, debug bool) error {
	dir, err := filepath.Abs(dirArg)
	if err != nil {
		return err
	}
	if isMounted(dir) {
		return userError("already_mounted", dir+" is already a mount point", "Run gs mount list, or gs unmount "+dir+" first.")
	}
	rec, err := r.mountRecordFor(ctx, sliceArg, dir, workspaceID)
	if err != nil {
		return err
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
	if err := r.startMountDaemon(rec); err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"id": rec.ID, "ref": rec.Account + ":" + rec.Slice, "mount_path": rec.MountPath, "base_commit": rec.BaseCommit, "files": rec.Files})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "mounted %s:%s at %s (read-only; %d files, fetched on first read)\n", rec.Account, rec.Slice, rec.MountPath, rec.Files)
		fmt.Fprintf(r.Stdout, "workspace %s; gs unmount %s to stop\n", rec.ID, rec.MountPath)
	}
	return nil
}

// mountRecordFor returns the workspace to mount at dir: the one named, the one
// last mounted at dir, or a new one for the slice.
func (r Runner) mountRecordFor(ctx context.Context, sliceArg, dir, workspaceID string) (mountRecord, error) {
	var rec mountRecord
	if workspaceID != "" {
		found, err := r.readMountRecord(workspaceID)
		if err != nil {
			return rec, userError("not_a_lazy_workspace", "no lazy workspace "+workspaceID, "Run gs mount list.")
		}
		found.MountPath = dir
		return found, r.writeMountRecord(found)
	}
	records, err := r.mountRecords()
	if err != nil {
		return rec, err
	}
	for _, existing := range records {
		if existing.MountPath == dir {
			if sliceArg != "" && !strings.EqualFold(strings.ReplaceAll(sliceArg, "/", ":"), existing.Account+":"+existing.Slice) && !strings.EqualFold(sliceArg, existing.Slice) {
				return rec, userError("mount_path_in_use", dir+" belongs to the lazy workspace of "+existing.Account+":"+existing.Slice, "Use another directory, or gs mount "+existing.Account+":"+existing.Slice+" "+dir+" to re-attach it.")
			}
			return existing, nil
		}
	}
	if sliceArg == "" {
		return rec, userError("slice_required", "no lazy workspace at "+dir, "Run gs mount <slice|account:slice> "+dir+".")
	}
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return rec, err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceArg)
	if err != nil {
		return rec, err
	}
	cache, err := r.objectCache()
	if err != nil {
		return rec, err
	}
	checkout, err := receiveCheckoutListing(callCtx, conn, ref, cache)
	if err != nil {
		return rec, err
	}
	entries := manifestEntries(checkout.entries)
	id, err := newMountID()
	if err != nil {
		return rec, err
	}
	state := r.mountStateDir(id)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return rec, err
	}
	if err := writeManifest(filepath.Join(state, "manifest"), entries); err != nil {
		return rec, err
	}
	files := 0
	for _, entry := range entries {
		if entry.Kind != lazyfs.KindDir {
			files++
		}
	}
	rec = mountRecord{
		ID:         id,
		Account:    checkout.slice.GetRef().GetAccount(),
		Slice:      checkout.slice.GetRef().GetSlice(),
		MountPath:  dir,
		ServerAddr: cfg.ServerAddr,
		BaseCommit: checkout.commitID,
		Files:      files,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	return rec, r.writeMountRecord(rec)
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

func callMountDaemon(id string, op string) (mountControlStatus, error) {
	var status mountControlStatus
	conn, err := net.DialTimeout("unix", mountControlPath(id), 2*time.Second)
	if err != nil {
		return status, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(mountControlRequest{Op: op}); err != nil {
		return status, err
	}
	err = json.NewDecoder(conn).Decode(&status)
	return status, err
}

func (r Runner) runUnmount(ctx context.Context, opts commandOptions, selector string) error {
	rec, err := r.findMountRecord(selector)
	if err != nil {
		return err
	}
	if !isMounted(rec.MountPath) {
		return userError("not_mounted", rec.MountPath+" is not mounted", "Its workspace is kept; gs mount list shows it.")
	}
	if _, err := callMountDaemon(rec.ID, "unmount"); err != nil {
		// No daemon to ask (it died): unmount directly.
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
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"id": rec.ID, "mount_path": rec.MountPath, "unmounted": true})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "unmounted %s; the workspace is kept (gs mount %s:%s %s re-attaches it)\n", rec.MountPath, rec.Account, rec.Slice, rec.MountPath)
	}
	return nil
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
	var status *mountControlStatus
	if mounted {
		if got, err := callMountDaemon(rec.ID, "status"); err == nil {
			status = &got
		}
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"id": rec.ID, "ref": rec.Account + ":" + rec.Slice, "mount_path": rec.MountPath, "mounted": mounted, "base_commit": rec.BaseCommit, "files": rec.Files, "daemon": status})
	}
	if opts.Quiet {
		return nil
	}
	state := "not mounted"
	if mounted {
		state = "mounted read-only"
	}
	fmt.Fprintf(r.Stdout, "%s:%s at %s: %s\n", rec.Account, rec.Slice, rec.MountPath, state)
	fmt.Fprintf(r.Stdout, "workspace %s, base commit %s, %d files\n", rec.ID, shortCommit(rec.BaseCommit), rec.Files)
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
	manifest, err := readManifest(filepath.Join(r.mountStateDir(rec.ID), "manifest"), rec.BaseCommit)
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
