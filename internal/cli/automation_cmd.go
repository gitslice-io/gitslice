package cli

import (
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
)

// automationCommand shows folder automations and their runs, and runs manual
// automations by hand (design/34_triggers.md).
func (r Runner) automationCommand(opts *commandOptions) *cobra.Command {
	autoCmd := &cobra.Command{
		Use:     "automation",
		Aliases: []string{"auto", "automations"},
		Short:   "Show folder automations and their runs; run manual automations",
		Long: "An automation is defined in <folder>/.gitslice/automations.yaml and runs on folder events or manually:\n" +
			"an HTTP call, a JS worker on Cloudflare, a bash container on Cloudflare, or folder checks. See gs help automations.",
		RunE: requireSubcommand("automation"),
	}
	markSchemaGroup(autoCmd)

	var lsRef string
	lsCmd := &cobra.Command{
		Use:     "ls [folder]",
		Aliases: []string{"list"},
		Short:   "List the automations that apply at a folder (default: the current one)",
		Args:    maxArgs(1, "gs automation ls [folder] [--ref <ref>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			folder := ""
			if len(args) == 1 {
				folder = args[0]
			}
			return r.runAutomationList(cmd.Context(), *opts, folder, lsRef)
		},
	}
	lsCmd.Flags().StringVar(&lsRef, "ref", "", "read the definitions at this ref (default refs/global/main)")

	var runsAccount, runsAuto, runsChangeset string
	var runsLimit int
	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "List automation runs of a changeset, an automation or an account, newest first",
		Args:  noArgs("gs automation runs [--changeset <id>] [--automation <id>] [--account <account>] [--limit n]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationRuns(cmd.Context(), *opts, runsAccount, runsAuto, runsChangeset, runsLimit)
		},
	}
	runsCmd.Flags().StringVar(&runsChangeset, "changeset", "", "runs about this changeset")
	runsCmd.Flags().StringVar(&runsAuto, "automation", "", "runs of this automation id, such as services/billing/deploy")
	runsCmd.Flags().StringVar(&runsAccount, "account", "", "the account (default: the workspace's account, else your active account)")
	runsCmd.Flags().IntVar(&runsLimit, "limit", 20, "how many runs")

	showCmd := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show an automation run",
		Args:  exactArgs(1, "gs automation show <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationShow(cmd.Context(), *opts, args[0])
		},
	}

	var logsFollow bool
	logsCmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print an automation run's log",
		Args:  exactArgs(1, "gs automation logs <run-id> [--follow]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationLogs(cmd.Context(), *opts, args[0], logsFollow)
		},
	}
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "stream logs until the run finishes")

	var runAccount, runRef, runChangeset string
	var runInputs []string
	var runYes, runFollow, runLocal bool
	var runFromChangeset string
	var runSecrets, runCallbacks []string
	runCmd := &cobra.Command{
		Use:   "run <automation-id>",
		Short: "Run an automation that listens for manual",
		Long: `Run an automation that listens for manual.

Before an automation lands, try it in one of two ways:

  --from-changeset <id>  run it on the server as defined in that open changeset
                         (an automation that lists secrets needs an account admin)
  --local                run a worker automation's script here with node, from
                         the workspace's files, with a stand-in gs; secrets come
                         from --secret NAME=value and callbacks from --callback`,
		Args: exactArgs(1, "gs automation run <automation-id> [--account <account>] [--ref <ref>] [--changeset <id> | --from-changeset <id>] [-i key=value]... [--yes] [--follow] [--local [--secret NAME=value]... [--callback body.json]...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runLocal {
				if runFromChangeset != "" || runChangeset != "" || runRef != "" {
					return userError("invalid_argument", "--local runs the workspace's files; it takes no --ref, --changeset or --from-changeset", "")
				}
				return r.runLocalAutomation(cmd.Context(), *opts, runAccount, args[0], runInputs, runSecrets, runCallbacks)
			}
			if len(runSecrets) > 0 || len(runCallbacks) > 0 {
				return userError("invalid_argument", "--secret and --callback go with --local", "")
			}
			if runFromChangeset != "" {
				if runChangeset != "" && runChangeset != runFromChangeset {
					return userError("invalid_argument", "--from-changeset already sets the changeset the run is about", "")
				}
				if runRef != "" {
					return userError("invalid_argument", "--from-changeset takes no --ref", "")
				}
			}
			return r.runAutomationRun(cmd.Context(), *opts, runAccount, args[0], runRef, runChangeset, runFromChangeset, runInputs, runYes, runFollow)
		},
	}
	runCmd.Flags().StringVar(&runAccount, "account", "", "the account (default: the workspace's account, else your active account)")
	runCmd.Flags().StringVar(&runRef, "ref", "", "run the definition at this ref (default refs/global/main)")
	runCmd.Flags().StringVar(&runChangeset, "changeset", "", "a changeset the run is about; its checks may be reported")
	runCmd.Flags().StringArrayVarP(&runInputs, "input", "i", nil, "key=value passed as automation input (repeatable)")
	runCmd.Flags().BoolVarP(&runYes, "yes", "y", false, "confirm manual.confirm prompt without asking")
	runCmd.Flags().BoolVarP(&runFollow, "follow", "f", false, "stream logs until the run finishes and exit non-zero on failure")
	runCmd.Flags().StringVar(&runFromChangeset, "from-changeset", "", "run the automation as defined in this open changeset, before it lands")
	runCmd.Flags().BoolVar(&runLocal, "local", false, "run a worker automation's script locally with node, from the workspace's files")
	runCmd.Flags().StringArrayVar(&runSecrets, "secret", nil, "with --local: NAME=value for a secret the automation lists (repeatable)")
	runCmd.Flags().StringArrayVar(&runCallbacks, "callback", nil, "with --local: a file whose contents are POSTed to onCallback after onEvent (repeatable, in order)")

	rerunCmd := &cobra.Command{
		Use:   "rerun <run-id>",
		Short: "Run a finished automation run again for the same event",
		Args:  exactArgs(1, "gs automation rerun <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationRunAction(cmd.Context(), *opts, args[0], "rerun")
		},
	}
	cancelCmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel an automation run that has not finished",
		Args:  exactArgs(1, "gs automation cancel <run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationRunAction(cmd.Context(), *opts, args[0], "cancel")
		},
	}

	var inspectAccount string
	inspectCmd := &cobra.Command{
		Use:   "inspect <uses-ref>",
		Short: "Inspect a reusable (on: [call]) automation template",
		Long: "Resolve a reusable automation template reference (/<account>[/<dir>...]/<name>[@<tag>|@sha256:...])\n" +
			"and show its inputs, secret slots, network requirements, resolved commit, and available immutable tags.",
		Args: exactArgs(1, "gs automation inspect <uses-ref> [--account <caller-account>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAutomationInspect(cmd.Context(), *opts, args[0], inspectAccount)
		},
	}
	inspectCmd.Flags().StringVar(&inspectAccount, "account", "", "caller account for unpinned same-account lookup")

	autoCmd.AddCommand(lsCmd, runsCmd, showCmd, logsCmd, runCmd, rerunCmd, cancelCmd, inspectCmd)
	return autoCmd
}

var triggerErrorHints = domainErrorOverrides{
	codes.Unimplemented: {
		Message: "this server does not run automations",
		Hint:    "Upgrade the server.",
	},
}

func automationError(err error) error {
	return withDomainErrorHints(err, triggerErrorHints)
}
