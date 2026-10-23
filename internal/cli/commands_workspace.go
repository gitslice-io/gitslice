package cli

import (
	"github.com/spf13/cobra"
)

// workspaceCommands builds gs init, gs workspace and gs sync.
func (r Runner) workspaceCommands(opts *commandOptions) []*cobra.Command {
	initLazy, initReadOnly := false, false
	var initPrefetch []string
	initCmd := &cobra.Command{
		Use:   "init <slice|account:slice> [dir]",
		Short: "Create a workspace for a slice: check it out here, or with --lazy mount it",
		Long: `Create a workspace for one slice.

Without --lazy, gs init checks the slice out into the current (empty)
directory: every file is downloaded, which grows with the slice.

With --lazy, gs init mounts the slice at dir (default: the current, empty
directory) as a lazy workspace, on Linux and macOS with FUSE. Only the listing
is fetched, which takes seconds and grows with the number of files, not their
size (2.6 s for 4,500 files, cold); a file's content is fetched the first time
something reads it, and kept in the local cache shared by every workspace on
this machine. A walk of the tree (grep -r, rg, a build) is read ahead of; for
work that reads everything, gs fetch first is fastest. Your edits stay on this machine, in the workspace's
local layer, until you run gs create. Every other command works as in a checkout
(status, diff, create, sync, switch). The workspace is served by one background
process shared by all your lazy workspaces, which exits when the last is
unmounted. After a reboot or gs unmount, gs mount <dir> brings the workspace back
with its edits. --prefetch <glob> (relative to the mount, repeatable) fetches
matching files in the background; --read-only refuses writes.`,
		Args: rangeArgs(1, 2, "gs init <slice|account:slice>  |  gs init --lazy <slice|account:slice> [dir]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !initLazy {
				if len(args) == 2 || initReadOnly || len(initPrefetch) > 0 {
					return userError("invalid_args", "a directory, --read-only and --prefetch apply to lazy workspaces only", "Add --lazy, or run gs init <slice> in the directory to check out.")
				}
				return r.runWorkspaceInit(cmd.Context(), *opts, args[0])
			}
			dir := ""
			if len(args) == 2 {
				dir = args[1]
			}
			var readOnlySetting *bool
			if cmd.Flags().Changed("read-only") {
				readOnlySetting = &initReadOnly
			}
			return r.runInitLazy(cmd.Context(), *opts, args[0], dir, readOnlySetting, initPrefetch)
		},
	}
	initCmd.Flags().BoolVar(&initLazy, "lazy", false, "mount the slice as a lazy workspace (FUSE) instead of checking it out")
	initCmd.Flags().BoolVar(&initReadOnly, "read-only", false, "with --lazy, refuse writes")
	initCmd.Flags().StringArrayVar(&initPrefetch, "prefetch", nil, "with --lazy, fetch files matching this glob (relative to the mount, such as 'acme/app/**/*.go') in the background; repeatable")

	workspaceCmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Manage the current single-slice workspace",
		RunE:    requireSubcommand("workspace"),
	}
	workspaceInitCmd := &cobra.Command{
		Use:    "init <slice|account:slice>",
		Short:  "Bind the current directory to a slice",
		Hidden: true,
		Args:   exactArgs(1, "gs workspace init <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWorkspaceInit(cmd.Context(), *opts, args[0])
		},
	}
	workspaceInitCmd.Flags().StringVar(&opts.AgentConversationID, "agent-conversation", "", "link changesets in this workspace to an agent conversation id")
	_ = workspaceInitCmd.Flags().MarkHidden("agent-conversation")
	workspaceHydrateCmd := &cobra.Command{
		Use:   "hydrate <path> [path...]",
		Short: "Hydrate workspace files through the client object cache",
		Args:  minArgs(1, "gs workspace hydrate <path> [path...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWorkspaceHydrate(cmd.Context(), *opts, args)
		},
	}
	workspaceSyncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the workspace to the latest remote base",
		Args:  noArgs("gs workspace sync"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWorkspaceSync(cmd.Context(), *opts)
		},
	}
	workspaceSyncCmd.Flags().StringVar(&opts.SyncMerge, "merge", defaultWorkspaceSyncMergeStrategy, "merge strategy for same-path changes: line, manual, ours, or theirs")
	workspaceDeleteForce := false
	workspaceDeleteCmd := &cobra.Command{
		Use:     "delete [dir]",
		Aliases: []string{"rm", "remove"},
		Short:   "Delete a workspace from this machine: unmount a lazy one, or remove a checkout",
		Long: `Delete a workspace from this machine (default: the one at the current
directory). A lazy workspace is unmounted, its local state (edits, checkpoints)
deleted, and its mount directory removed if it is empty. A checkout's directory
is removed, with everything in it.

It refuses while the workspace has local edits that no changeset holds, and
names them: run gs create to keep them, or pass --force to discard them.
Changesets already created or submitted are on the server and are not touched.
Pass the workspace's own directory, not a folder inside it.`,
		Args: maxArgs(1, "gs workspace delete [dir] [--force]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := ""
			if len(args) > 0 {
				dir = args[0]
			}
			return r.runWorkspaceDelete(cmd.Context(), *opts, dir, workspaceDeleteForce)
		},
	}
	workspaceDeleteCmd.Flags().BoolVar(&workspaceDeleteForce, "force", false, "delete even when there are local edits no changeset holds, discarding them")
	workspaceCmd.AddCommand(workspaceInitCmd, workspaceHydrateCmd, workspaceSyncCmd, workspaceDeleteCmd)

	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the workspace to the latest remote base",
		Args:  noArgs("gs sync"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWorkspaceSync(cmd.Context(), *opts)
		},
	}
	syncCmd.Flags().StringVar(&opts.SyncMerge, "merge", defaultWorkspaceSyncMergeStrategy, "merge strategy for same-path changes: line, manual, ours, or theirs")

	return []*cobra.Command{initCmd, workspaceCmd, syncCmd}
}

// statusCommands builds gs status and gs context.
func (r Runner) statusCommands(opts *commandOptions) []*cobra.Command {
	statusCmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Show workspace changes against the local base snapshot",
		Args:    noArgs("gs status [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runStatus(cmd.Context(), *opts)
		},
	}
	contextCmd := &cobra.Command{
		Use:     "context",
		Aliases: []string{"ctx"},
		Short:   "Show resolved CLI context",
		Args:    noArgs("gs context [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runContext(cmd.Context(), *opts)
		},
	}

	return []*cobra.Command{statusCmd, contextCmd}
}
