package cli

import (
	"github.com/spf13/cobra"
)

// workspaceCommands builds gs init, gs workspace and gs sync.
func (r Runner) workspaceCommands(opts *commandOptions) []*cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init <slice|account:slice>",
		Short: "Bind the current directory to a slice",
		Args:  exactArgs(1, "gs init <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runWorkspaceInit(cmd.Context(), *opts, args[0])
		},
	}

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
	workspaceCmd.AddCommand(workspaceInitCmd, workspaceHydrateCmd, workspaceSyncCmd)

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
