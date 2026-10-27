package cli

import (
	"fmt"

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
	sliceCreateDescription := ""
	sliceCreateCmd := &cobra.Command{
		Use:   "create <slice|account:slice>",
		Short: "Create a slice",
		Args:  exactArgs(1, "gs slice create <slice|account:slice> [--include /account/path] [--visibility private|public] [--required-approvals n] [--required-check name] [--description text]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceCreate(cmd.Context(), *opts, args[0], sliceCreateIncludes, sliceCreateVisibility, sliceCreateRequiredApprovals, sliceCreateRequiredChecks, sliceCreateDescription)
		},
	}
	sliceCreateCmd.Flags().StringArrayVar(&sliceCreateIncludes, "include", nil, "included global path; repeat for multiple paths")
	sliceCreateCmd.Flags().StringVar(&sliceCreateVisibility, "visibility", sliceCreateVisibility, "slice visibility: private or public")
	sliceCreateCmd.Flags().IntVar(&sliceCreateRequiredApprovals, "required-approvals", sliceCreateRequiredApprovals, "distinct non-author approvals required before submit")
	sliceCreateCmd.Flags().StringArrayVar(&sliceCreateRequiredChecks, "required-check", nil, "check name required to pass before submit; repeat for multiple checks")
	sliceCreateCmd.Flags().StringVar(&sliceCreateDescription, "description", "", "a short note shown on the slice's page, such as what it holds or which project it mirrors (up to 500 characters)")
	sliceListShared := false
	sliceListArchived := false
	sliceListCmd := &cobra.Command{
		Use:   "list [account]",
		Short: "List slices in an account, and those shared with you",
		Args:  maxArgs(1, "gs slice list [account] [--shared] [--archived]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) > 0 {
				account = args[0]
			}
			return r.runSliceList(cmd.Context(), *opts, account, sliceListShared, sliceListArchived)
		},
	}
	sliceListCmd.Flags().BoolVar(&sliceListShared, "shared", false, "only the slices shared with you from other accounts")
	sliceListCmd.Flags().BoolVar(&sliceListArchived, "archived", false, "include archived slices")
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
	sliceUpdateMergeOnSubmit := ""
	var sliceUpdateIncludes []string
	var sliceUpdateRequiredChecks []string
	sliceUpdateDescription := ""
	sliceUpdateCmd := &cobra.Command{
		Use:   "update <slice|account:slice>",
		Short: "Update slice included paths, visibility, submit settings or description",
		Args:  exactArgs(1, "gs slice update <slice|account:slice> [--include /account/path] [--visibility private|public] [--required-approvals n] [--required-check name] [--description text] [--merge-on-submit on|off]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			visibilityChanged := cmd.Flags().Changed("visibility")
			includesChanged := cmd.Flags().Changed("include")
			requiredApprovalsChanged := cmd.Flags().Changed("required-approvals")
			requiredChecksChanged := cmd.Flags().Changed("required-check")
			var description *string
			if cmd.Flags().Changed("description") {
				description = &sliceUpdateDescription
			}
			var mergeOnSubmit *bool
			if cmd.Flags().Changed("merge-on-submit") {
				enabled, err := parseOnOff(sliceUpdateMergeOnSubmit)
				if err != nil {
					return userError("invalid_args", "--merge-on-submit takes on or off", "Use --merge-on-submit on to merge stale changesets at submit, or off to refuse them.")
				}
				mergeOnSubmit = &enabled
			}
			return r.runSliceUpdate(cmd.Context(), *opts, args[0], sliceUpdateIncludes, includesChanged, sliceUpdateVisibility, visibilityChanged, sliceUpdateRequiredApprovals, requiredApprovalsChanged, sliceUpdateRequiredChecks, requiredChecksChanged, sliceUpdateClearRequiredChecks, description, mergeOnSubmit)
		},
	}
	sliceUpdateCmd.Flags().StringArrayVar(&sliceUpdateIncludes, "include", nil, "replacement included global path; repeat for multiple paths")
	sliceUpdateCmd.Flags().StringVar(&sliceUpdateVisibility, "visibility", sliceUpdateVisibility, "slice visibility: private or public")
	sliceUpdateCmd.Flags().IntVar(&sliceUpdateRequiredApprovals, "required-approvals", sliceUpdateRequiredApprovals, "replacement distinct non-author approvals required before submit")
	sliceUpdateCmd.Flags().StringArrayVar(&sliceUpdateRequiredChecks, "required-check", nil, "replacement required check name; repeat for multiple checks")
	sliceUpdateCmd.Flags().BoolVar(&sliceUpdateClearRequiredChecks, "clear-required-checks", sliceUpdateClearRequiredChecks, "remove all required checks")
	sliceUpdateCmd.Flags().StringVar(&sliceUpdateDescription, "description", "", "replacement description shown on the slice's page; --description '' clears it")
	sliceUpdateCmd.Flags().StringVar(&sliceUpdateMergeOnSubmit, "merge-on-submit", "", "on: a changeset whose base went stale is merged onto the latest base at submit (a new patchset that reruns CI); off: it is refused until synced")
	sliceDeleteYes := false
	sliceDeleteCmd := &cobra.Command{
		Use:   "delete <slice|account:slice>",
		Short: "Delete a slice",
		Long: "Delete a slice. A slice that has changesets can be deleted too when none of them is open and every path it " +
			"includes is covered by another slice or holds no files: the changesets stay, so history stays readable " +
			"on the paths, and the name stays reserved. To hide a slice but keep it, use gs slice archive.",
		Args: exactArgs(1, "gs slice delete <slice|account:slice> --yes"),
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
		Short: "Manage the CI secrets of one slice",
		Long: "These commands manage the secrets that reach one slice. A secret shared with other slices,\n" +
			"or with every slice of the account, is managed with gs secret.",
	}
	sliceSecretSetCmd := &cobra.Command{
		Use:   "set <slice|account:slice> <NAME> [value]",
		Short: "Set a CI secret of this slice only, reading the value from stdin",
		Args:  rangeArgs(2, 3, "gs slice secret set <slice|account:slice> <NAME> < value"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 3 {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: a value on the command line is kept in shell history; pipe it in instead: printf %s \"$VALUE\" | gs slice secret set "+args[0]+" "+args[1])
				return r.runSliceSecretSet(cmd.Context(), *opts, args[0], args[1], &args[2])
			}
			return r.runSliceSecretSet(cmd.Context(), *opts, args[0], args[1], nil)
		},
	}
	sliceSecretRemoveCmd := &cobra.Command{
		Use:     "rm <slice|account:slice> <NAME>",
		Aliases: []string{"delete", "remove"},
		Short:   "Remove a CI secret from this slice",
		Args:    exactArgs(2, "gs slice secret rm <slice|account:slice> <NAME>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceSecretRemove(cmd.Context(), *opts, args[0], args[1])
		},
	}
	sliceSecretListCmd := &cobra.Command{
		Use:   "ls <slice|account:slice>",
		Short: "List the names of the CI secrets that reach this slice",
		Args:  exactArgs(1, "gs slice secret ls <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSliceSecretList(cmd.Context(), *opts, args[0])
		},
	}
	sliceSecretCmd.AddCommand(sliceSecretSetCmd, sliceSecretRemoveCmd, sliceSecretListCmd)
	sliceCmd.AddCommand(r.sliceMemberCommands(opts)...)
	sliceCmd.AddCommand(sliceCreateCmd, sliceListCmd, sliceInfoCmd, slicePathsCmd, sliceHistoryCmd, sliceUpdateCmd, sliceSetCIDaemonCmd, sliceSecretCmd, sliceDeleteCmd)
	sliceCmd.AddCommand(r.sliceLifecycleCommands(opts)...)

	return sliceCmd
}
