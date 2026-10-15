package cli

import (
	"github.com/spf13/cobra"
)

// changesetCommand builds gs cs (gs changeset) and its subcommands.
func (r Runner) changesetCommand(opts *commandOptions) *cobra.Command {
	csCmd := &cobra.Command{
		Use:     "cs",
		Aliases: []string{"changeset"},
		Short:   "Manage changesets",
		Hidden:  true,
		RunE:    requireSubcommand("cs"),
	}
	createTitle := "CLI changeset"
	csCreateEmpty := emptyChangesetOptions{}
	csCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a changeset from workspace edits",
		Args:  noArgs("gs cs create [--title title] [--if-changed | --allow-empty]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetCreate(cmd.Context(), *opts, createTitle, csCreateEmpty)
		},
	}
	csCreateCmd.Flags().StringVar(&createTitle, "title", createTitle, "changeset title")
	csCreateEmpty.addFlags(csCreateCmd)
	captureTitle := "agent changeset"
	captureNoChecks := false
	csCaptureCmd := &cobra.Command{
		Use:    "capture",
		Short:  "Capture current workspace edits as a patchset (create or update)",
		Hidden: true,
		Args:   noArgs("gs cs capture [--title title]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetCapture(cmd.Context(), *opts, captureTitle, captureNoChecks)
		},
	}
	csCaptureCmd.Flags().StringVar(&captureTitle, "title", captureTitle, "changeset title used when creating")
	csCaptureCmd.Flags().BoolVar(&captureNoChecks, "no-checks", captureNoChecks, "skip capture-time checks")
	csUpdateCmd := &cobra.Command{
		Use:   "update",
		Short: "Create a new patchset for the current changeset",
		Args:  noArgs("gs cs update"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetUpdate(cmd.Context(), *opts)
		},
	}
	csSubmitNoWatch := false
	csSubmitWatchTimeout := "5m"
	csSubmitCmd := &cobra.Command{
		Use:   "submit [changeset]",
		Short: "Submit the current changeset",
		Args:  maxArgs(1, "gs cs submit [changeset] [--no-watch] [--watch-timeout 5m]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			watchTimeout, err := parsePositiveDurationFlag("watch-timeout", csSubmitWatchTimeout)
			if err != nil {
				return err
			}
			return r.runChangesetSubmit(cmd.Context(), *opts, id, !csSubmitNoWatch, watchTimeout)
		},
	}
	csSubmitCmd.Flags().BoolVar(&csSubmitNoWatch, "no-watch", csSubmitNoWatch, "return after submit is accepted without waiting for publish")
	csSubmitCmd.Flags().StringVar(&csSubmitWatchTimeout, "watch-timeout", csSubmitWatchTimeout, "maximum time to wait for required checks that have not reported, and for publish")
	csStatusWatch := false
	csStatusWatchTimeout := "5m"
	csStatusCmd := &cobra.Command{
		Use:   "status [changeset]",
		Short: "Show the current changeset status",
		Args:  maxArgs(1, "gs cs status [changeset] [--watch] [--watch-timeout 5m] [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			watchTimeout, err := parsePositiveDurationFlag("watch-timeout", csStatusWatchTimeout)
			if err != nil {
				return err
			}
			return r.runChangesetStatus(cmd.Context(), *opts, id, csStatusWatch, watchTimeout)
		},
	}
	csStatusCmd.Flags().BoolVar(&csStatusWatch, "watch", csStatusWatch, "wait until the changeset reaches a terminal submitted state")
	csStatusCmd.Flags().StringVar(&csStatusWatchTimeout, "watch-timeout", csStatusWatchTimeout, "maximum time to wait for status changes")
	csShowCmd := &cobra.Command{
		Use:   "show [changeset]",
		Short: "Show changeset details",
		Args:  maxArgs(1, "gs cs show [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetShow(cmd.Context(), *opts, id)
		},
	}
	csLinkCmd := &cobra.Command{
		Use:   "link [changeset]",
		Short: "Print a changeset's web link, checked to exist (default: the workspace's changeset)",
		Args:  maxArgs(1, "gs cs link [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetLink(cmd.Context(), *opts, id)
		},
	}
	csExplainCmd := &cobra.Command{
		Use:   "explain [changeset]",
		Short: "Explain changeset validation inputs",
		Args:  maxArgs(1, "gs cs explain [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetExplain(cmd.Context(), *opts, id)
		},
	}
	csVersionsCmd := &cobra.Command{
		Use:     "versions [changeset]",
		Aliases: []string{"patchsets"},
		Short:   "List patchsets for a changeset",
		Args:    maxArgs(1, "gs cs versions [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetVersions(cmd.Context(), *opts, id)
		},
	}
	csConversationPatchset := ""
	csConversationCmd := &cobra.Command{
		Use:   "conversation [changeset]",
		Short: "Show the agent conversation that produced each patchset",
		Args:  maxArgs(1, "gs cs conversation [changeset] [--patchset n]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetConversation(cmd.Context(), *opts, id, csConversationPatchset)
		},
	}
	csConversationCmd.Flags().StringVar(&csConversationPatchset, "patchset", csConversationPatchset, "only show the conversation for this patchset number")

	csDiffPatchset := ""
	csDiffFrom := ""
	csDiffTo := ""
	csDiffNameOnly := false
	csDiffStat := false
	csDiffCmd := &cobra.Command{
		Use:   "diff [changeset]",
		Short: "Show a server-side changeset diff",
		Args:  maxArgs(1, "gs cs diff [changeset] [--patchset n|id] [--from n|id --to n|id]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetDiff(cmd.Context(), *opts, id, csDiffPatchset, csDiffFrom, csDiffTo, csDiffNameOnly, csDiffStat)
		},
	}
	csDiffCmd.Flags().StringVar(&csDiffPatchset, "patchset", csDiffPatchset, "patchset number or id to diff against its base")
	csDiffCmd.Flags().StringVar(&csDiffFrom, "from", csDiffFrom, "patchset number or id to diff from")
	csDiffCmd.Flags().StringVar(&csDiffTo, "to", csDiffTo, "patchset number or id to diff to")
	csDiffCmd.Flags().BoolVar(&csDiffNameOnly, "name-only", csDiffNameOnly, "show only changed path names")
	csDiffCmd.Flags().BoolVar(&csDiffStat, "stat", csDiffStat, "show a compact changed-path summary")
	csApproveCmd := &cobra.Command{
		Use:   "approve <changeset>",
		Short: "Approve the current patchset of a changeset",
		Args:  exactArgs(1, "gs cs approve <changeset>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetApprove(cmd.Context(), *opts, args[0])
		},
	}
	csCheckStatus := ""
	csCheckCmd := &cobra.Command{
		Use:   "check <changeset> <check-name>",
		Short: "Report a changeset check result",
		Args:  exactArgs(2, "gs cs check <changeset> <check-name> --status pass|fail"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetCheck(cmd.Context(), *opts, args[0], args[1], csCheckStatus)
		},
	}
	csCheckCmd.Flags().StringVar(&csCheckStatus, "status", csCheckStatus, "check status: pass or fail")
	csAbandonReason := ""
	csAbandonCmd := &cobra.Command{
		Use:   "abandon [changeset]",
		Short: "Abandon a changeset",
		Args:  maxArgs(1, "gs cs abandon [changeset]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return r.runChangesetAbandon(cmd.Context(), *opts, id, csAbandonReason)
		},
	}
	csAbandonCmd.Flags().StringVar(&csAbandonReason, "reason", csAbandonReason, "abandon reason")
	csListSlice := ""
	csListStatus := ""
	csListLimit := 20
	csListCmd := &cobra.Command{
		Use:   "list",
		Short: "List changesets for a slice",
		Args:  noArgs("gs cs list [--slice slice|account:slice] [--status status]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runChangesetList(cmd.Context(), *opts, csListSlice, csListStatus, csListLimit)
		},
	}
	csListCmd.Flags().StringVar(&csListSlice, "slice", csListSlice, "authoring slice, defaults to current workspace slice")
	csListCmd.Flags().StringVar(&csListStatus, "status", csListStatus, "status filter")
	csListCmd.Flags().IntVar(&csListLimit, "limit", csListLimit, "maximum changesets to list")
	csCmd.AddCommand(csCreateCmd, csCaptureCmd, csUpdateCmd, csSubmitCmd, csStatusCmd, csShowCmd, csLinkCmd, csExplainCmd, csVersionsCmd, csDiffCmd, csConversationCmd, csApproveCmd, csCheckCmd, csAbandonCmd, csListCmd)

	return csCmd
}
