package cli

import (
	"github.com/spf13/cobra"
)

// mountCommands builds gs mount (with list and status), gs unmount and gs
// fetch: lazy workspaces (design/30_lazy_workspaces.md).
func (r Runner) mountCommands(opts *commandOptions) []*cobra.Command {
	foreground, workspaceID, debug := false, "", false
	mountCmd := &cobra.Command{
		Use:   "mount <slice|account:slice> <dir>",
		Short: "Mount a slice lazily: listing now, file content on first read",
		Long: `Mount a slice at a directory as a lazy workspace. Only the listing is fetched,
so mounting takes about a second at any slice size; a file's content is fetched
the first time something reads it, together with small files beside it, and
kept in the local cache (shared by every workspace on this machine).

This release mounts read-only, on Linux and macOS (with FUSE). Mounting the
same directory again re-attaches the same workspace; --workspace <id>
re-attaches one at another path. gs mount list shows them all.`,
		Args: rangeArgs(1, 2, "gs mount <slice|account:slice> <dir>  |  gs mount --workspace <id> <dir>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			slice, dir := "", args[0]
			if len(args) == 2 {
				slice, dir = args[0], args[1]
			}
			return r.runMount(cmd.Context(), *opts, slice, dir, workspaceID, foreground, debug)
		},
	}
	mountCmd.Flags().BoolVar(&foreground, "foreground", false, "serve the mount in this process instead of a background daemon")
	mountCmd.Flags().StringVar(&workspaceID, "workspace", "", "re-attach the lazy workspace with this id (from gs mount list)")
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
	mountCmd.AddCommand(listCmd, statusCmd)

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
	return []*cobra.Command{mountCmd, unmountCmd, fetchCmd}
}
