package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/treestore"
)

type adminError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e adminError) Error() string {
	if e.Hint == "" {
		return e.Message
	}
	return e.Message + "\nhint: " + e.Hint
}

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	jsonMode := false
	root := newRootCommand(stdout, stderr, &jsonMode)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(context.Background()); err != nil {
		if jsonMode {
			var aErr adminError
			if !errors.As(err, &aErr) {
				aErr = adminError{Code: "command_failed", Message: err.Error()}
			}
			enc := json.NewEncoder(stderr)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"error": aErr})
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	return 0
}

func newRootCommand(stdout, stderr io.Writer, jsonMode *bool) *cobra.Command {
	format := "text"
	jsonFlag := false
	root := &cobra.Command{
		Use:           "gitslice-admin",
		Short:         "Gitslice server storage administration and repair commands",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if jsonFlag || format == "json" {
				*jsonMode = true
			}
			if format != "text" && format != "json" {
				return adminError{Code: "invalid_format", Message: "invalid output format " + format, Hint: "Use --format text, --format json, or --json."}
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&format, "format", "text", "output format: text or json")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false, "emit JSON output")

	rebuildYes := false
	rebuildTargetRef := model.DefaultTargetRef
	rebuildCmd := &cobra.Command{
		Use:   "rebuild-indexes",
		Short: "Rebuild derived indexes from source-of-truth commits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdminRebuildIndexes(cmd.Context(), stdout, *jsonMode, rebuildTargetRef, rebuildYes)
		},
	}
	rebuildCmd.Flags().StringVar(&rebuildTargetRef, "target-ref", rebuildTargetRef, "target ref to rebuild")
	rebuildCmd.Flags().BoolVar(&rebuildYes, "yes", rebuildYes, "confirm derived index rebuild")

	gcDryRun := false
	gcSampleLimit := 0
	gcCmd := &cobra.Command{
		Use:   "gc",
		Short: "Report unreachable storage objects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdminGC(cmd.Context(), stdout, *jsonMode, gcDryRun, gcSampleLimit)
		},
	}
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", gcDryRun, "report only; required because deletion is future work")
	gcCmd.Flags().IntVar(&gcSampleLimit, "sample-limit", gcSampleLimit, "maximum sample ids to print per category")

	root.AddCommand(rebuildCmd, gcCmd)
	return root
}

func runAdminRebuildIndexes(ctx context.Context, stdout io.Writer, jsonOutput bool, targetRef string, yes bool) error {
	if !yes {
		return adminError{
			Code:    "confirmation_required",
			Message: "rebuild-indexes requires --yes",
			Hint:    "Run gitslice-admin rebuild-indexes --yes after confirming no conflicting repair is running.",
		}
	}
	if strings.TrimSpace(targetRef) == "" {
		targetRef = model.DefaultTargetRef
	}
	db, objectStore, err := openAdminStores(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	_ = objectStore
	if err := db.Changesets().RebuildDerivedIndexes(ctx, targetRef); err != nil {
		return err
	}
	out := map[string]any{
		"target_ref": targetRef,
		"rebuilt":    true,
	}
	if jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Fprintf(stdout, "rebuilt derived indexes for %s\n", targetRef)
	return nil
}

func openAdminStores(ctx context.Context) (*postgres.DB, *filesystem.Store, error) {
	databaseURL := os.Getenv("GITSLICE_DATABASE_URL")
	if databaseURL == "" {
		return nil, nil, adminError{
			Code:    "missing_config",
			Message: "GITSLICE_DATABASE_URL is required",
			Hint:    "Set GITSLICE_DATABASE_URL to the metadata database used by the server.",
		}
	}
	objectStoreRoot := os.Getenv("GITSLICE_OBJECT_STORE_ROOT")
	if objectStoreRoot == "" {
		return nil, nil, adminError{
			Code:    "missing_config",
			Message: "GITSLICE_OBJECT_STORE_ROOT is required",
			Hint:    "Set GITSLICE_OBJECT_STORE_ROOT to the filesystem object store root used by the server.",
		}
	}
	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return nil, nil, err
	}
	objectStore, err := filesystem.New(objectStoreRoot)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	db.SetTreeStore(treestore.New(objectStore))
	return db, objectStore, nil
}

func runAdminGC(ctx context.Context, stdout io.Writer, jsonOutput bool, dryRun bool, sampleLimit int) error {
	if !dryRun {
		return adminError{
			Code:    "dry_run_required",
			Message: "admin gc requires --dry-run",
			Hint:    "Deletion is future work; run gitslice-admin gc --dry-run to produce a report.",
		}
	}
	db, objectStore, err := openAdminStores(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	report, err := db.ReportUnreachable(ctx, objectStore, postgres.GCOptions{SampleLimit: sampleLimit})
	if err != nil {
		return err
	}
	if jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	printAdminGCReport(stdout, report)
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
