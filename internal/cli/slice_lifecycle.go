package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// sliceLifecycleCommands builds gs slice rename, archive and unarchive.
func (r Runner) sliceLifecycleCommands(opts *commandOptions) []*cobra.Command {
	renameCmd := &cobra.Command{
		Use:   "rename <slice|account:slice> <new-name>",
		Short: "Rename a slice",
		Long: "Rename a slice within its account. Its history, members, settings and changesets move with it. " +
			"The old name keeps working: it resolves to the slice, so Git URLs, Go import paths and existing " +
			"workspaces do not break, and it stays reserved so no other slice can take it.",
		Args: exactArgs(2, "gs slice rename <slice|account:slice> <new-name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceRename(cmd.Context(), *opts, args[0], args[1])
		},
	}
	archiveCmd := &cobra.Command{
		Use:   "archive <slice|account:slice>",
		Short: "Archive a slice",
		Long: "Hide a slice from listings and make it read-only. Its history stays readable and its name stays " +
			"reserved. gs slice unarchive restores it; gs slice list --archived lists archived slices.",
		Args: exactArgs(1, "gs slice archive <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceArchive(cmd.Context(), *opts, args[0], true)
		},
	}
	unarchiveCmd := &cobra.Command{
		Use:   "unarchive <slice|account:slice>",
		Short: "Restore an archived slice",
		Args:  exactArgs(1, "gs slice unarchive <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceArchive(cmd.Context(), *opts, args[0], false)
		},
	}
	return []*cobra.Command{renameCmd, archiveCmd, unarchiveCmd}
}

func (r Runner) runSliceRename(ctx context.Context, opts commandOptions, sliceRef, newName string) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	newName, err = normalizeCLISlug(newName, "new slice name")
	if err != nil {
		return err
	}
	client := corev1.NewSliceServiceClient(conn)
	renamed, err := client.RenameSlice(callCtx, &corev1.RenameSliceRequest{Slice: ref, NewName: newName})
	if err != nil {
		return err
	}
	r.followRenamedWorkspace(ref, renamed.GetRef())
	if opts.jsonOutput() {
		out := sliceToOutput(renamed)
		return r.writeJSONOutput(opts, struct {
			sliceOutput
			PreviousRef string `json:"previous_ref"`
		}{out, sliceRefLabel(ref)})
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "renamed slice %s to %s\n", sliceRefLabel(ref), sliceRefLabel(renamed.GetRef()))
	fmt.Fprintf(r.Stdout, "the old name %s still resolves to it and stays reserved\n", sliceRefLabel(ref))
	return nil
}

func (r Runner) runSliceArchive(ctx context.Context, opts commandOptions, sliceRef string, archived bool) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}
	updated, err := corev1.NewSliceServiceClient(conn).SetSliceArchived(callCtx, &corev1.SetSliceArchivedRequest{Slice: ref, Archived: archived})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"id":       updated.Id,
			"ref":      sliceRefLabel(updated.Ref),
			"archived": updated.Archived,
		})
	}
	if opts.Quiet {
		return nil
	}
	if archived {
		fmt.Fprintf(r.Stdout, "archived slice %s: hidden from listings and read-only; gs slice unarchive restores it\n", sliceRefLabel(updated.Ref))
	} else {
		fmt.Fprintf(r.Stdout, "unarchived slice %s\n", sliceRefLabel(updated.Ref))
	}
	return nil
}

// followRenamedWorkspace points the current workspace at the slice's new name
// when it was made from the old one. The old name keeps working, so a failure
// here is not worth reporting.
func (r Runner) followRenamedWorkspace(old, renamed *corev1.SliceRef) {
	root, err := r.workspaceRoot()
	if err != nil {
		return
	}
	var cfg WorkspaceConfig
	path := filepath.Join(root, ".gs", "slice.json")
	if err := readJSONFile(path, &cfg); err != nil {
		return
	}
	if cfg.Account != old.GetAccount() || cfg.Slice != old.GetSlice() {
		return
	}
	cfg.Slice = renamed.GetSlice()
	_ = writeJSONFile(path, cfg, 0o644)
}
