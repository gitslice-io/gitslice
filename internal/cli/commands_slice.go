package cli

import (
	"github.com/spf13/cobra"
)

// sliceCommand builds gs slice and its subcommands, including the sharing commands.
func (r Runner) sliceCommand(opts *commandOptions) *cobra.Command {
	sliceCmd := &cobra.Command{
		Use:     "slice",
		Aliases: []string{"slices"},
		Short:   "Manage slices",
		RunE:    requireSubcommand("slice"),
	}
	sliceCreateVisibility := "private"
	sliceCreateRequiredApprovals := 0
	var sliceCreateIncludes []string
	var sliceCreateRequiredChecks []string
	sliceCreateCmd := &cobra.Command{
		Use:   "create <slice|account:slice>",
		Short: "Create a slice",
		Args:  exactArgs(1, "gs slice create <slice|account:slice> [--include /account/path] [--visibility private|public] [--required-approvals n] [--required-check name]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceCreate(cmd.Context(), *opts, args[0], sliceCreateIncludes, sliceCreateVisibility, sliceCreateRequiredApprovals, sliceCreateRequiredChecks)
		},
	}
	sliceCreateCmd.Flags().StringArrayVar(&sliceCreateIncludes, "include", nil, "included global path; repeat for multiple paths")
	sliceCreateCmd.Flags().StringVar(&sliceCreateVisibility, "visibility", sliceCreateVisibility, "slice visibility: private or public")
	sliceCreateCmd.Flags().IntVar(&sliceCreateRequiredApprovals, "required-approvals", sliceCreateRequiredApprovals, "distinct non-author approvals required before submit")
	sliceCreateCmd.Flags().StringArrayVar(&sliceCreateRequiredChecks, "required-check", nil, "check name required to pass before submit; repeat for multiple checks")
	sliceListShared := false
	sliceListCmd := &cobra.Command{
		Use:   "list [account]",
		Short: "List slices in an account, and those shared with you",
		Args:  maxArgs(1, "gs slice list [account] [--shared]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) > 0 {
				account = args[0]
			}
			return r.runSliceList(cmd.Context(), *opts, account, sliceListShared)
		},
	}
	sliceListCmd.Flags().BoolVar(&sliceListShared, "shared", false, "only the slices shared with you from other accounts")
	sliceInfoCmd := &cobra.Command{
		Use:   "info <slice|account:slice>",
		Short: "Show slice metadata",
		Args:  exactArgs(1, "gs slice info <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceInfo(cmd.Context(), *opts, args[0])
		},
	}
	slicePathsCmd := &cobra.Command{
		Use:   "paths <slice|account:slice>",
		Short: "Show slice included paths",
		Args:  exactArgs(1, "gs slice paths <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSlicePaths(cmd.Context(), *opts, args[0])
		},
	}
	sliceHistoryPageSize := 50
	sliceHistoryCmd := &cobra.Command{
		Use:   "history <slice|account:slice>",
		Short: "Show slice definition history",
		Args:  exactArgs(1, "gs slice history <slice|account:slice> [--page-size n]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceHistory(cmd.Context(), *opts, args[0], sliceHistoryPageSize)
		},
	}
	sliceHistoryCmd.Flags().IntVar(&sliceHistoryPageSize, "page-size", sliceHistoryPageSize, "maximum definition versions to print")
	sliceUpdateVisibility := ""
	sliceUpdateRequiredApprovals := 0
	sliceUpdateClearRequiredChecks := false
	var sliceUpdateIncludes []string
	var sliceUpdateRequiredChecks []string
	sliceUpdateCmd := &cobra.Command{
		Use:   "update <slice|account:slice>",
		Short: "Update slice included paths, visibility, or submit settings",
		Args:  exactArgs(1, "gs slice update <slice|account:slice> [--include /account/path] [--visibility private|public] [--required-approvals n] [--required-check name]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			visibilityChanged := cmd.Flags().Changed("visibility")
			includesChanged := cmd.Flags().Changed("include")
			requiredApprovalsChanged := cmd.Flags().Changed("required-approvals")
			requiredChecksChanged := cmd.Flags().Changed("required-check")
			return r.runSliceUpdate(cmd.Context(), *opts, args[0], sliceUpdateIncludes, includesChanged, sliceUpdateVisibility, visibilityChanged, sliceUpdateRequiredApprovals, requiredApprovalsChanged, sliceUpdateRequiredChecks, requiredChecksChanged, sliceUpdateClearRequiredChecks)
		},
	}
	sliceUpdateCmd.Flags().StringArrayVar(&sliceUpdateIncludes, "include", nil, "replacement included global path; repeat for multiple paths")
	sliceUpdateCmd.Flags().StringVar(&sliceUpdateVisibility, "visibility", sliceUpdateVisibility, "slice visibility: private or public")
	sliceUpdateCmd.Flags().IntVar(&sliceUpdateRequiredApprovals, "required-approvals", sliceUpdateRequiredApprovals, "replacement distinct non-author approvals required before submit")
	sliceUpdateCmd.Flags().StringArrayVar(&sliceUpdateRequiredChecks, "required-check", nil, "replacement required check name; repeat for multiple checks")
	sliceUpdateCmd.Flags().BoolVar(&sliceUpdateClearRequiredChecks, "clear-required-checks", sliceUpdateClearRequiredChecks, "remove all required checks")
	sliceDeleteYes := false
	sliceDeleteCmd := &cobra.Command{
		Use:   "delete <slice|account:slice>",
		Short: "Delete a slice",
		Args:  exactArgs(1, "gs slice delete <slice|account:slice> --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceDelete(cmd.Context(), *opts, args[0], sliceDeleteYes)
		},
	}
	sliceDeleteCmd.Flags().BoolVar(&sliceDeleteYes, "yes", sliceDeleteYes, "confirm slice deletion")
	sliceSetCIDaemonCmd := &cobra.Command{
		Use:   "set-ci-daemon <slice|account:slice> <daemon-id>",
		Short: "Set the slice CI daemon",
		Args:  exactArgs(2, "gs slice set-ci-daemon <slice|account:slice> <daemon-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceSetCIDaemon(cmd.Context(), *opts, args[0], args[1])
		},
	}
	sliceSecretCmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage slice CI secrets",
	}
	sliceSecretSetCmd := &cobra.Command{
		Use:   "set <slice|account:slice> <NAME> <value>",
		Short: "Set a slice CI secret",
		Args:  exactArgs(3, "gs slice secret set <slice|account:slice> <NAME> <value>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceSecretSet(cmd.Context(), *opts, args[0], args[1], args[2])
		},
	}
	sliceSecretRemoveCmd := &cobra.Command{
		Use:     "rm <slice|account:slice> <NAME>",
		Aliases: []string{"delete", "remove"},
		Short:   "Remove a slice CI secret",
		Args:    exactArgs(2, "gs slice secret rm <slice|account:slice> <NAME>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceSecretRemove(cmd.Context(), *opts, args[0], args[1])
		},
	}
	sliceSecretListCmd := &cobra.Command{
		Use:   "ls <slice|account:slice>",
		Short: "List slice CI secret names",
		Args:  exactArgs(1, "gs slice secret ls <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceSecretList(cmd.Context(), *opts, args[0])
		},
	}
	sliceSecretCmd.AddCommand(sliceSecretSetCmd, sliceSecretRemoveCmd, sliceSecretListCmd)
	sliceCmd.AddCommand(r.sliceMemberCommands(opts)...)
	sliceCmd.AddCommand(sliceCreateCmd, sliceListCmd, sliceInfoCmd, slicePathsCmd, sliceHistoryCmd, sliceUpdateCmd, sliceSetCIDaemonCmd, sliceSecretCmd, sliceDeleteCmd)

	return sliceCmd
}
