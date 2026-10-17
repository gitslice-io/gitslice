package cli

import (
	"github.com/spf13/cobra"
)

// mountCommands builds gs mount (with list and status), gs unmount and gs
// fetch: lazy workspaces (design/30_lazy_workspaces.md).
func (r Runner) mountCommands(opts *commandOptions) []*cobra.Command {
	foreground, workspaceID, debug, readOnly := false, "", false, false
	mountCmd := &cobra.Command{
		Use:   "mount <slice|account:slice> <dir>",
		Short: "Mount a slice lazily: listing now, file content on first read, edits kept local",
		Long: `Mount a slice at a directory as a lazy workspace (Linux and macOS, with FUSE).
Only the listing is fetched, so mounting takes about a second at any slice size;
a file's content is fetched the first time something reads it, together with
small files beside it, and kept in the local cache (shared by every workspace on
this machine).

Edit with any tool. Edits stay in the workspace's local layer, on this machine,
until you run gs create or gs modify: nothing is uploaded before. gs status and
gs diff work as in a gs init workspace; gs restore drops local edits. gs sync and
switching between changesets do not work in a mount yet.

Mounting the same directory again re-attaches the same workspace, edits
included; --workspace <id> re-attaches one at another path. gs mount list shows
them all; gs mount discard deletes one.`,
		Args: rangeArgs(1, 2, "gs mount <slice|account:slice> <dir>  |  gs mount --workspace <id> <dir>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			slice, dir := "", args[0]
			if len(args) == 2 {
				slice, dir = args[0], args[1]
			}
			// --read-only changes the workspace only when given: the daemon's
			// own gs mount --foreground leaves what the record says.
			var readOnlySetting *bool
			if cmd.Flags().Changed("read-only") {
				readOnlySetting = &readOnly
			}
			return r.runMount(cmd.Context(), *opts, slice, dir, workspaceID, foreground, debug, readOnlySetting)
		},
	}
	mountCmd.Flags().BoolVar(&foreground, "foreground", false, "serve the mount in this process instead of a background daemon")
	mountCmd.Flags().StringVar(&workspaceID, "workspace", "", "re-attach the lazy workspace with this id (from gs mount list)")
	mountCmd.Flags().BoolVar(&readOnly, "read-only", false, "refuse writes")
	mountCmd.Flags().BoolVar(&debug, "debug-fuse", false, "log every FUSE request (with --foreground)")
	_ = mountCmd.Flags().MarkHidden("debug-fuse")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List the lazy workspaces on this machine, mounted or not",
		Args:  noArgs("gs mount list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runMountList(*opts)
		},
	}
	statusCmd := &cobra.Command{
		Use:   "status [dir|id]",
		Short: "Show a lazy workspace's mount: base commit, fetches, errors",
		Args:  maxArgs(1, "gs mount status [dir|id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			selector := ""
			if len(args) > 0 {
				selector = args[0]
			}
			return r.runMountStatus(cmd.Context(), *opts, selector)
		},
	}
	discardYes := false
	discardCmd := &cobra.Command{
		Use:   "discard <dir|id>",
		Short: "Delete an unmounted lazy workspace and its local edits",
		Args:  exactArgs(1, "gs mount discard <dir|id> --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runMountDiscard(cmd.Context(), *opts, args[0], discardYes)
		},
	}
	discardCmd.Flags().BoolVar(&discardYes, "yes", false, "confirm: the local edits are deleted for good")
	mountCmd.AddCommand(listCmd, statusCmd, discardCmd)

	unmountCmd := &cobra.Command{
		Use:   "unmount <dir|id>",
		Short: "Unmount a lazy workspace; the workspace is kept",
		Args:  exactArgs(1, "gs unmount <dir|id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runUnmount(cmd.Context(), *opts, args[0])
		},
	}
	fetchWorkspace := ""
	fetchCmd := &cobra.Command{
		Use:   "fetch [path...]",
		Short: "Fetch a lazy workspace's files into the local cache, for offline work or a build",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runMountFetch(cmd.Context(), *opts, fetchWorkspace, args)
		},
	}
	fetchCmd.Flags().StringVar(&fetchWorkspace, "workspace", "", "the lazy workspace (id or directory); default: the one around the current directory")
	restoreAll := false
	restoreCmd := &cobra.Command{
		Use:   "restore <path>... | --all",
		Short: "Drop local edits in a lazy workspace, so the base shows again",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runRestore(cmd.Context(), *opts, args, restoreAll)
		},
	}
	restoreCmd.Flags().BoolVar(&restoreAll, "all", false, "drop every local edit")
	return []*cobra.Command{mountCmd, unmountCmd, fetchCmd, restoreCmd}
}
