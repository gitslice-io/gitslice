package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/agentd"
	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type agentStartOptions struct {
	Name        string
	Runtime     string
	RuntimeSet  bool
	CI          bool
	Slices      []string
	IdleTimeout time.Duration
	Lazy        bool
	DetachAfter time.Duration
	DetachSet   bool
}

type agentDaemonOutput struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Runtime     string   `json:"runtime"`
	Status      string   `json:"status"`
	LastSeenAt  string   `json:"last_seen_at"`
	DisplayName string   `json:"display_name,omitempty"`
	Account     string   `json:"account,omitempty"`
	Mine        bool     `json:"mine"`
	CISlices    []string `json:"ci_slices,omitempty"`
	Manageable  bool     `json:"manageable"`
	ExposedTo   []string `json:"exposed_to,omitempty"`
}

func (d agentDaemonOutput) shownName() string { return firstNonEmpty(d.DisplayName, d.Name) }

func agentDaemonToOutput(daemon *corev1.AgentDaemon) agentDaemonOutput {
	out := agentDaemonOutput{
		ID:          daemon.GetId(),
		Name:        daemon.GetName(),
		DisplayName: daemon.GetDisplayName(),
		Account:     daemon.GetAccount(),
		Runtime:     daemon.GetRuntime(),
		Status:      daemon.GetStatus(),
		LastSeenAt:  daemon.GetLastSeenAt(),
		Mine:        daemon.GetMine(),
		Manageable:  daemon.GetManageable(),
	}
	for _, ref := range daemon.GetExposedTo() {
		out.ExposedTo = append(out.ExposedTo, sliceRefLabel(ref))
	}
	for _, ref := range daemon.GetCiSlices() {
		out.CISlices = append(out.CISlices, sliceRefLabel(ref))
	}
	return out
}

type agentStatusOutput struct {
	Daemons []agentDaemonOutput `json:"daemons"`
}

func (r Runner) agentCommand(opts *commandOptions) *cobra.Command {
	start := agentStartOptions{Runtime: agentd.DefaultRuntimeName}
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Run and inspect local BYOA agent daemons",
		RunE:  requireSubcommand("agent"),
	}
	markSchemaGroup(agentCmd)
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start a local agent daemon",
		Args:  noArgs("gs agent start [--name NAME] [--runtime codex] [--slice account/slice]... [--ci]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			start.RuntimeSet = cmd.Flags().Changed("runtime")
			start.DetachSet = cmd.Flags().Changed("detach-after")
			return r.runAgentStart(cmd.Context(), *opts, start)
		},
	}
	startCmd.Flags().StringVar(&start.Name, "name", start.Name, "name the daemon registers with; starting again with the same name finds the same daemon, with the slices that use it and the name gs agent rename gave it")
	startCmd.Flags().StringVar(&start.Runtime, "runtime", start.Runtime, "agent runtime")
	startCmd.Flags().StringArrayVar(&start.Slices, "slice", nil, "limit agent conversations to a slice (<account>/<slice>); repeatable")
	startCmd.Flags().DurationVar(&start.IdleTimeout, "idle-timeout", start.IdleTimeout, "disconnect and exit after this much conversation inactivity (0 = never)")
	startCmd.Flags().BoolVar(&start.Lazy, "lazy", start.Lazy, "mount each conversation's workspace lazily (gs mount) instead of checking it out; falls back to a checkout without FUSE")
	startCmd.Flags().DurationVar(&start.DetachAfter, "detach-after", agentd.DefaultDetachAfter, "checks-only daemons (--ci): detach from the server after this long with nothing to run, and poll for queued checks (0 = stay connected; env GITSLICE_AGENT_DETACH_AFTER). The server then has no stream to hold open and can scale to zero")
	startCmd.Flags().BoolVar(&start.CI, "ci", false, "run checks only: no agent runtime, any directory")
	_ = startCmd.Flags().MarkHidden("idle-timeout")
	_ = startCmd.Flags().MarkHidden("lazy")

	statusSlice := ""
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "List agent daemons for the signed-in account",
		Long: `List the daemons you started and those of agents you own, with the slices that
use each as their CI daemon and the slices each is exposed to. With --slice, also
list the daemons their owners exposed to that slice: the ones you can choose from
with gs slice set-ci-daemon.`,
		Args: noArgs("gs agent status [--slice <slice>] [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAgentStatus(cmd.Context(), *opts, statusSlice)
		},
	}
	statusCmd.Flags().StringVar(&statusSlice, "slice", "", "also list the daemons exposed to this slice, which you own or administer")

	exposeCmd := &cobra.Command{
		Use:   "expose <daemon> <slice>",
		Short: "Let a slice's owners choose a daemon of yours as its CI daemon",
		Long: `A CI daemon runs a slice's checks and receives its secrets, so you decide which
slices yours is exposed to. Once you expose it, the owners and admins of that
slice can choose it with gs slice set-ci-daemon. You must be able to read the
slice. gs agent unexpose withdraws it: the slice's checks are then refused until
it names another daemon.`,
		Args: exactArgs(2, "gs agent expose <daemon> <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAgentExpose(cmd.Context(), *opts, args[0], args[1], true)
		},
	}
	unexposeCmd := &cobra.Command{
		Use:   "unexpose <daemon> <slice>",
		Short: "Stop offering a daemon of yours to a slice",
		Args:  exactArgs(2, "gs agent unexpose <daemon> <slice|account:slice>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAgentExpose(cmd.Context(), *opts, args[0], args[1], false)
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Explain how to stop the local agent daemon",
		Args:  noArgs("gs agent stop"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAgentStop(*opts)
		},
	}

	renameClear := false
	renameCmd := &cobra.Command{
		Use:   "rename <daemon> [new-name]",
		Short: "Name an agent daemon, shown in the web app, slice settings and check runs",
		Long: `Give an agent daemon a name to be shown by. The daemon is found by its id or by
the name it shows (gs agent status lists them). The name it registers with, gs
agent start --name, is unchanged, so restarting the daemon finds the same one and
keeps the new name. Names are unique among an account's daemons, up to 64
characters. --clear shows the registered name again.

You can rename the daemons you started and those of agents you own.`,
		Args: rangeArgs(1, 2, "gs agent rename <daemon> <new-name>  |  gs agent rename <daemon> --clear"),
		RunE: func(cmd *cobra.Command, args []string) error {
			newName := ""
			if len(args) == 2 {
				newName = args[1]
			}
			if (len(args) == 2) == renameClear {
				return userError("invalid_args", "give a new name, or --clear to drop the name", "Usage: gs agent rename <daemon> <new-name> | gs agent rename <daemon> --clear")
			}
			return r.runAgentRename(cmd.Context(), *opts, args[0], newName)
		},
	}
	renameCmd.Flags().BoolVar(&renameClear, "clear", false, "drop the name, showing the registered one again")

	agentCmd.AddCommand(r.agentCreateCommand(opts), startCmd, statusCmd, stopCmd, renameCmd, exposeCmd, unexposeCmd, r.agentDoctorCommand(opts), r.agentInstallCommand(opts), r.agentUninstallCommand(opts))
	return agentCmd
}

func (r Runner) runAgentStart(ctx context.Context, opts commandOptions, in agentStartOptions) error {
	if in.CI && in.Lazy {
		return userError("invalid_args", "--ci cannot be used with --lazy", "Run checks-only daemons without --lazy.")
	}
	if in.CI && in.RuntimeSet {
		return userError("invalid_args", "--ci cannot be used with --runtime", "Checks-only daemons always register runtime ci.")
	}
	if in.CI && len(in.Slices) > 0 {
		return userError("invalid_args", "--ci cannot be used with --slice", "Checks-only daemons select slices via gs agent expose / gs slice set-ci-daemon.")
	}
	allowedSlices, err := normalizeDaemonAllowedSlices(in.Slices)
	if err != nil {
		return err
	}
	if in.CI {
		checks := r.agentDoctorChecks(ctx, true)
		if !opts.Quiet {
			for _, check := range checks {
				if check.Status != "ok" {
					fmt.Fprintf(r.stderr(), "%s  %s  %s\n", check.Status, check.Name, check.Detail)
				}
			}
		}
	}
	idleTimeout := in.IdleTimeout
	if idleTimeout <= 0 {
		if value, ok := os.LookupEnv("GITSLICE_AGENT_IDLE_TIMEOUT"); ok {
			if parsed, err := time.ParseDuration(value); err == nil {
				idleTimeout = parsed
			}
		}
	}

	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	if !in.CI {
		if err := r.requireAgentWorkspaceDir(); err != nil {
			return err
		}
	}

	runtimeName := agentd.CIRuntimeName
	var runtime agentd.Runtime
	if !in.CI {
		runtimeName = strings.TrimSpace(in.Runtime)
		if runtimeName == "" {
			runtimeName = agentd.DefaultRuntimeName
		}
		runtime, err = agentd.RuntimeForName(runtimeName, cliVersionInfo().Version)
		if err != nil {
			return err
		}
	}

	root := r.cwd()
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}

	detachAfter, err := agentd.DetachAfterFor(in.CI, in.DetachSet, in.DetachAfter)
	if err != nil {
		return err
	}

	signalCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	daemon := agentd.NewDaemon(signalCtx, r.agentHost(cfg, opts))
	daemon.Runtime = runtime
	daemon.WorkingDir = root
	daemon.Lazy = in.Lazy
	daemon.Name = in.Name
	daemon.AllowedSlices = allowedSlices
	daemon.DetachAfter = detachAfter
	daemon.Quiet = opts.Quiet
	return daemon.Run(signalCtx, runtimeName, idleTimeout, in.CI)
}

func (r Runner) agentHost(cfg UserConfig, opts commandOptions) agentd.HostCallbacks {
	return agentd.HostCallbacks{
		ServerAddr:          cfg.ServerAddr,
		Token:               cfg.Token,
		Home:                r.Home,
		Insecure:            r.Insecure || opts.Insecure,
		Version:             cliVersionInfo().Version,
		Stdout:              r.Stdout,
		Stderr:              r.stderr(),
		Dial:                dial,
		DialStream:          dialAgentStream,
		DialLazy:            dialLazy,
		AuthContext:         func(ctx context.Context) context.Context { return authContext(ctx, cfg) },
		UsesNonLoopbackTLS:  serverUsesNonLoopbackTLS,
		InsecureFromContext: insecureFromContext,
		EnvServerAddr:       envServerAddrOverride,
		WorkspaceInit: func(ctx context.Context, workdir, conversationID, sliceRef string, envOverrides map[string]string) (string, error) {
			var stderr bytes.Buffer
			child := r.agentChildRunner(cfg, workdir, envOverrides, io.Discard)
			child.Stderr = io.MultiWriter(&stderr, r.stderr())
			err := child.runWorkspaceInit(ctx, commandOptions{Format: "text", Quiet: true}, sliceRef, conversationID)
			return strings.TrimSpace(stderr.String()), err
		},
		CaptureChangeset: func(ctx context.Context, workdir string, envOverrides map[string]string, title string) (bool, string, error) {
			child := r.agentChildRunner(cfg, workdir, envOverrides, io.Discard)
			res, err := child.captureChangeset(ctx, commandOptions{Format: "text", Verbose: true}, title, false)
			if err != nil {
				return false, "", err
			}
			return res.Outcome == captureCaptured, res.Summary(), nil
		},
		ObjectCache: r.objectCache,
		FetchCheckBatch: func(ctx context.Context, rf agentd.BatchReadClient, cache *clientcache.ObjectCache, rootTreeID string, targets []agentd.BatchReadTarget, workers int) error {
			readTargets := make([]remoteReadTarget, len(targets))
			for i, t := range targets {
				readTargets[i] = remoteReadTarget{Path: t.Path, ContentHash: t.ContentHash, Size: t.Size}
			}
			_, err := NewTransferManager(rf, nil, nil, cache).FetchFilesToCache(ctx, TransferFetchRequest{
				RootTreeID: rootTreeID,
				Targets:    readTargets,
				Workers:    workers,
			})
			return err
		},
		CheckTreeWriter: agentd.CheckTreeWriter{
			EnsureDir: ensureConfinedDir,
			WriteFile: writeConfinedWorkspaceFile,
		},
		ReportOnline: func(daemonID string) error {
			if opts.JSONOutput() {
				return r.writeJSONOutput(opts, map[string]string{
					"daemon_id": daemonID,
					"status":    "online",
				})
			}
			if !opts.Quiet {
				fmt.Fprintf(r.Stdout, "agent daemon %s online\n", daemonID)
			}
			return nil
		},
	}
}

// agentChildRunner scopes a Runner to a conversation's directory and identity
// while hiding the host's own credentials.
func (r Runner) agentChildRunner(cfg UserConfig, dir string, envOverrides map[string]string, stdout io.Writer) Runner {
	env := map[string]string{}
	if addr := strings.TrimSpace(cfg.ServerAddr); addr != "" {
		env["GS_SERVER_ADDR"] = addr
		env["GITSLICE_SERVER_ADDR"] = addr
	}
	if len(envOverrides) == 0 {
		if token := strings.TrimSpace(cfg.Token); token != "" {
			env["GS_TOKEN"] = token
		}
	}
	for key, value := range envOverrides {
		env[key] = value
	}
	child := r
	child.Dir = dir
	child.Stdout = stdout
	child.Stderr = r.stderr()
	child.Stdin = strings.NewReader("")
	child.Env = env
	if home := strings.TrimSpace(env["HOME"]); home != "" {
		child.Home = home
	}
	return child
}

func normalizeDaemonAllowedSlices(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		ref, err := parseSliceRef(raw)
		if err != nil {
			return nil, err
		}
		key := agentd.CanonicalSliceKey(ref.GetAccount(), ref.GetSlice())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out, nil
}

func (r Runner) agentDoctorCommand(opts *commandOptions) *cobra.Command {
	ci := false
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this machine can run an agent daemon",
		Args:  noArgs("gs agent doctor [--ci] [--json]"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.runAgentDoctor(cmd.Context(), *opts, ci)
		},
	}
	cmd.Flags().BoolVar(&ci, "ci", false, "check a checks-only CI daemon")
	return cmd
}

func (r Runner) runAgentDoctor(ctx context.Context, opts commandOptions, ci bool) error {
	checks := r.agentDoctorChecks(ctx, ci)
	if opts.JSONOutput() {
		if err := r.writeJSONOutput(opts, agentd.DoctorOutput{Checks: checks}); err != nil {
			return err
		}
	} else if !opts.Quiet {
		for _, check := range checks {
			fmt.Fprintf(r.stdout(), "%s  %s  %s\n", check.Status, check.Name, check.Detail)
		}
	}
	return agentd.DoctorFailure(checks)
}

func (r Runner) agentDoctorChecks(ctx context.Context, ci bool) []agentd.DoctorCheck {
	cfg, cfgErr := r.readPartialUserConfig()
	return agentd.RunDoctorChecks(ctx, ci, agentd.DoctorConfig{
		Token:         cfg.Token,
		SubjectID:     cfg.SubjectID,
		ConfigPath:    r.userConfigPath(),
		ConfigErr:     cfgErr,
		DescribeToken: describeToken,
	}, agentd.MergeDoctorProbes(r.AgentDoctorProbes))
}

func (r Runner) agentInstallCommand(opts *commandOptions) *cobra.Command {
	in := agentd.InstallOptions{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install an agent daemon as a user service",
		Args:  noArgs("gs agent install [--ci] [--name NAME] [--slice account/slice]... [--print]"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.runAgentInstall(cmd.Context(), *opts, in)
		},
	}
	cmd.Flags().BoolVar(&in.CI, "ci", false, "install a checks-only daemon")
	cmd.Flags().StringVar(&in.Name, "name", "", "daemon name (defaults to this machine name)")
	cmd.Flags().StringArrayVar(&in.Slices, "slice", nil, "limit agent conversations to a slice (<account>/<slice>); repeatable")
	cmd.Flags().BoolVar(&in.Print, "print", false, "print the service definition without installing it")
	return cmd
}

func (r Runner) agentUninstallCommand(opts *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall [NAME]",
		Short: "Stop and remove an installed agent daemon",
		Args:  maxArgs(1, "gs agent uninstall [NAME]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return r.runAgentUninstall(cmd.Context(), *opts, name)
		},
	}
}

func (r Runner) runAgentInstall(ctx context.Context, opts commandOptions, in agentd.InstallOptions) error {
	if in.CI && len(in.Slices) > 0 {
		return userError("invalid_args", "--ci cannot be used with --slice", "Checks-only daemons select slices via gs agent expose / gs slice set-ci-daemon.")
	}
	allowedSlices, err := normalizeDaemonAllowedSlices(in.Slices)
	if err != nil {
		return err
	}
	workingDir, err := r.agentServiceWorkingDir(in.CI)
	if err != nil {
		return err
	}
	return agentd.InstallService(ctx, agentd.MergeServiceProbes(r.AgentService), r.homeDir(), workingDir, in, allowedSlices, opts.Quiet, r.stdout())
}

func (r Runner) runAgentUninstall(ctx context.Context, opts commandOptions, requestedName string) error {
	return agentd.UninstallService(ctx, agentd.MergeServiceProbes(r.AgentService), r.homeDir(), requestedName, opts.Quiet, r.stdout())
}

func (r Runner) agentServiceWorkingDir(ci bool) (string, error) {
	if ci {
		return filepath.Abs(r.homeDir())
	}
	if err := r.requireAgentWorkspaceDir(); err != nil {
		return "", err
	}
	return filepath.Abs(r.cwd())
}
