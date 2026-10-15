package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/treestore"
)

func (r Runner) runAdminRebuildIndexes(ctx context.Context, opts commandOptions, targetRef string, yes bool) error {
	if !yes {
		return userError("confirmation_required", "rebuild-indexes requires --yes", "Run gs admin rebuild-indexes --yes after confirming no conflicting repair is running.")
	}
	if strings.TrimSpace(targetRef) == "" {
		targetRef = postgres.DefaultTargetRef
	}
	databaseURL := os.Getenv("GITSLICE_DATABASE_URL")
	if databaseURL == "" {
		return userError("missing_config", "GITSLICE_DATABASE_URL is required", "Set GITSLICE_DATABASE_URL to the metadata database used by the server.")
	}
	objectStoreRoot := os.Getenv("GITSLICE_OBJECT_STORE_ROOT")
	if objectStoreRoot == "" {
		return userError("missing_config", "GITSLICE_OBJECT_STORE_ROOT is required", "Set GITSLICE_OBJECT_STORE_ROOT to the filesystem object store root used by the server.")
	}
	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	objectStore, err := filesystem.New(objectStoreRoot)
	if err != nil {
		return err
	}
	db.SetTreeStore(treestore.New(objectStore))
	if err := db.Changesets().RebuildDerivedIndexes(ctx, targetRef); err != nil {
		return err
	}
	out := map[string]any{
		"target_ref": targetRef,
		"rebuilt":    true,
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	fmt.Fprintf(r.Stdout, "rebuilt derived indexes for %s\n", targetRef)
	return nil
}

func (r Runner) runAdminGC(ctx context.Context, opts commandOptions, dryRun bool, sampleLimit int) error {
	if !dryRun {
		return userError("dry_run_required", "admin gc requires --dry-run", "Deletion is future work; run gs admin gc --dry-run to produce a report.")
	}
	databaseURL := os.Getenv("GITSLICE_DATABASE_URL")
	if databaseURL == "" {
		return userError("missing_config", "GITSLICE_DATABASE_URL is required", "Set GITSLICE_DATABASE_URL to the metadata database used by the server.")
	}
	objectStoreRoot := os.Getenv("GITSLICE_OBJECT_STORE_ROOT")
	if objectStoreRoot == "" {
		return userError("missing_config", "GITSLICE_OBJECT_STORE_ROOT is required", "Set GITSLICE_OBJECT_STORE_ROOT to the filesystem object store root used by the server.")
	}
	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	objectStore, err := filesystem.New(objectStoreRoot)
	if err != nil {
		return err
	}
	db.SetTreeStore(treestore.New(objectStore))
	report, err := db.ReportUnreachable(ctx, objectStore, postgres.GCOptions{SampleLimit: sampleLimit})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, report)
	}
	printAdminGCReport(r.Stdout, report)
	return nil
}

func printAdminGCReport(w io.Writer, report postgres.GCReport) {
	fmt.Fprintln(w, "storage lifecycle report (dry-run)")
	fmt.Fprintf(w, "roots: refs=%d live_changesets=%d pending_publish=%d\n", report.Roots.Refs, report.Roots.LiveChangesets, report.Roots.PendingPublish)
	fmt.Fprintf(w, "blobs: total=%d reachable=%d orphan=%d\n", report.BlobCount, report.ReachableBlobCount, report.OrphanBlobCount)
	printAdminGCSamples(w, "orphan blob samples", report.OrphanBlobs)
	if report.TreeNodeEnumerationLimited {
		fmt.Fprintln(w, "tree nodes: orphan enumeration limited")
	} else {
		fmt.Fprintf(w, "tree nodes: reachable=%d orphan=%d\n", report.ReachableTreeCount, report.OrphanTreeCount)
		printAdminGCSamples(w, "orphan tree node samples", report.OrphanTreeNodes)
	}
	fmt.Fprintf(w, "abandoned patchsets: count=%d\n", report.AbandonedPatchsetCount)
	printAdminGCSamples(w, "abandoned patchset samples", report.AbandonedPatchsets)
	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
}

func printAdminGCSamples(w io.Writer, label string, ids []string) {
	if len(ids) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", label)
	for _, id := range ids {
		fmt.Fprintf(w, "  %s\n", id)
	}
}
