package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/agentd"
)

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Dir    string
	Home   string
	// Insecure allows plaintext connections to non-loopback servers, as
	// --insecure does; it is per Runner, so concurrent runs never share it.
	Insecure bool
	// Env overrides process environment variables for the settings Runner
	// reads through Getenv. When non-nil it also hides the host's credentials
	// (such as GS_TOKEN) that Env does not name, so an isolated child run only
	// sees the identity it was given.
	Env map[string]string
	// AgentDoctorProbes and AgentService are optional host-operation seams for
	// the agent preflight and user-service commands. They are primarily useful
	// to unit tests; nil uses the real machine.
	AgentDoctorProbes *agentd.DoctorProbes
	AgentService      *agentd.ServiceProbes
	changesetWait     *changesetWaitConfig
}

type commandOptions struct {
	Format         string
	JSONFields     []string
	JQ             string
	Template       string
	Quiet          bool
	NonInteractive bool
	NoColor        bool
	Verbose        bool
	Debug          bool
	Trace          bool
	Insecure       bool
	SyncMerge      string
	// AgentConversationID, when set on `workspace init`, stamps the workspace
	// config so changesets authored here link to the agent conversation.
	AgentConversationID string
	// NoSubmit leaves the changeset a gs fs write creates open for review
	// instead of landing it.
	NoSubmit bool
	// DiffContext is -U/--unified of the diff commands: the unchanged lines
	// shown around each change. DiffContextSet tells it was given.
	DiffContext    int
	DiffContextSet bool
}

func (o commandOptions) jsonOutput() bool {
	return o.Format == "json" || len(o.JSONFields) > 0 || o.JQ != "" || o.Template != ""
}

func Main(args []string, stdout, stderr io.Writer) int {
	r := Runner{Stdout: stdout, Stderr: stderr, Stdin: os.Stdin}
	if err := r.Run(context.Background(), args); err != nil {
		if wantsJSON(args) {
			if writeErr := writeJSON(stderr, classifyError(err)); writeErr != nil {
				fmt.Fprintln(stderr, err)
			}
		} else {
			fmt.Fprintln(stderr, err)
		}
		return exitCodeForError(err)
	}
	return 0
}

func (r Runner) Run(ctx context.Context, args []string) error {
	ctx = withInsecure(ctx, r.Insecure)
	args, dir, err := extractDirectoryFlag(args, r.cwd())
	if err != nil {
		return err
	}
	if dir != "" {
		r.Dir = dir
	}
	expanded, err := r.expandAliasArgs(args)
	if err != nil {
		return r.enhanceCommandError(err)
	}
	root := r.rootCommand()
	root.SetArgs(expanded)
	root.SetOut(r.Stdout)
	root.SetErr(r.Stderr)
	update := r.startUpdateCheck(ctx, expanded)
	err = r.enhanceCommandError(root.ExecuteContext(ctx))
	update.finish(r)
	r.maybePruneObjectCache()
	return err
}

func (r Runner) rootCommand() *cobra.Command {
	opts := &commandOptions{Format: "text", SyncMerge: defaultWorkspaceSyncMergeStrategy}
	jsonFlagValue := ""

	root := &cobra.Command{
		Use:           "gs",
		Short:         "Gitslice native CLI",
		Example:       "  gs auth signup --username nico\n  gs shell\n  gs fs upload ./notes /nico/notes --recursive\n  gs init nico/home\n  gs status\n  gs create --message \"update notes\"\n  gs diff\n  gs submit",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return userError("missing_command", "missing command", "Run gs --help to list available commands.")
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			r.Insecure = r.Insecure || opts.Insecure
			cmd.SetContext(withInsecure(cmd.Context(), r.Insecure))
			if cmd.Root().PersistentFlags().Changed("json") {
				opts.Format = "json"
				fields, err := parseJSONFields(jsonFlagValue)
				if err != nil {
					return err
				}
				opts.JSONFields = fields
			}
			if opts.Format != "text" && opts.Format != "json" && opts.Format != "medium" {
				return userError("invalid_format", "invalid output format "+opts.Format, "Use --format text, --format json, --format medium where supported, or --json.")
			}
			if opts.JQ != "" && opts.Template != "" {
				return userError("invalid_format", "cannot use --jq and --template together", "Use --jq to filter JSON or --template to format output.")
			}
			if !opts.Quiet && !opts.jsonOutput() {
				cmd.SetContext(withProgress(cmd.Context(), r.stderr()))
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&opts.Format, "format", "text", "output format: text, json, or command-specific formats such as medium")
	root.PersistentFlags().StringVar(&jsonFlagValue, "json", "", "emit JSON output; optionally select comma-separated fields with --json=field,field")
	root.PersistentFlags().Lookup("json").NoOptDefVal = "*"
	root.PersistentFlags().StringVar(&opts.JQ, "jq", "", "filter structured output with a jq expression")
	root.PersistentFlags().StringVar(&opts.Template, "template", "", "format structured output with a Go template")
	root.PersistentFlags().BoolVar(&opts.Quiet, "quiet", false, "suppress non-essential text output")
	root.PersistentFlags().BoolVar(&opts.NonInteractive, "non-interactive", false, "fail instead of prompting for input")
	root.PersistentFlags().BoolVar(&opts.NoColor, "no-color", false, "disable colorized output")
	root.PersistentFlags().BoolVar(&opts.Verbose, "verbose", false, "emit additional diagnostic output")
	root.PersistentFlags().BoolVar(&opts.Debug, "debug", false, "emit debug diagnostics")
	root.PersistentFlags().BoolVar(&opts.Trace, "trace", false, "emit trace diagnostics")
	root.PersistentFlags().BoolVar(&opts.Insecure, "insecure", false, "allow plaintext connections to non-loopback servers")
	// -C is applied by Run before the command tree is built (extractDirectoryFlag);
	// it is declared here so help lists it.
	root.PersistentFlags().StringP("directory", "C", "", "run as if gs was started in this directory (like git -C); repeatable, each relative to the last")
	root.SetHelpCommand(r.helpCommand(root))
	root.AddCommand(
		r.authCommand(opts),
		r.claimsCommand(opts),
		r.sliceCommand(opts),
		r.agentCommand(opts),
		r.accountCommand(opts),
		r.groupCommand(opts),
		r.tagCommand(opts),
		r.intentCommand(opts),
		r.changesetCommand(opts),
		r.checkCommand(opts),
		r.fsCommand(opts),
		r.importCommand(opts),
		r.shellCommand(opts),
		r.configCommand(opts),
		r.aliasCommand(opts),
		r.rpcCommand(opts),
		r.browseCommand(opts),
		r.versionCommand(opts),
		r.schemaCommand(opts),
		r.upgradeCommand(opts),
		r.doctorCommand(opts),
		r.feedbackCommand(opts),
		r.mcpCommand(opts),
		r.blameCommand(opts),
		r.policyCommand(opts),
		r.secretCommand(opts),
		r.triggerCommand(opts),
	)
	for _, commands := range [][]*cobra.Command{
		r.workspaceCommands(opts),
		r.mountCommands(opts),
		r.statusCommands(opts),
		r.historyCommands(opts),
		r.stackCommands(opts),
	} {
		root.AddCommand(commands...)
	}
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	if comp, _, err := root.Find([]string{"completion"}); err == nil && comp != nil && comp != root {
		withSchemaCollapsed(comp, "gs completion <shell>", "print a shell completion script (bash, zsh, fish, powershell); changeset and slice arguments complete from the server")
	}
	r.attachCompletions(root)
	return root
}
