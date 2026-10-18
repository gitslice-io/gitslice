package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const (
	defaultAgentRuntime = "codex"
	ciAgentRuntime      = "ci"
	agentSendBuffer     = 128
	agentHeartbeat      = 15 * time.Second
	// The server sends Ping messages every 20s. A 50s inbound gap is safely above
	// normal jitter but short enough to catch half-open server->daemon streams
	// before queued pushes wait on the transport's much longer receive timeout.
	agentInboundStaleTimeout       = 50 * time.Second
	agentInboundStaleCheckInterval = 10 * time.Second
	agentMetaFilename              = ".agent-meta.json"
	agentRecentCheckRunLimit       = 256
	// agentMaxPendingEvents bounds a conversation's unacked outbound buffer. Acks
	// are per-event and keep this near-empty in practice; the cap only guards the
	// degenerate case where the server stops acking.
	agentMaxPendingEvents = 10000
	// agentTurnCompleteType marks the end of an agent turn. A turn can emit
	// several finalized messages/tool calls before it finishes, so this explicit
	// control event — not a finalized message — is the reliable turn-end signal
	// for clients (which render it invisibly).
	agentTurnCompleteType = "turn_complete"
)

type agentStartOptions struct {
	Name        string
	Runtime     string
	RuntimeSet  bool
	CI          bool
	IdleTimeout time.Duration
	// Lazy mounts each conversation's workspace (gs mount) instead of
	// checking it out, where FUSE is available.
	Lazy bool
}

type agentDaemonOutput struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Runtime    string `json:"runtime"`
	Status     string `json:"status"`
	LastSeenAt string `json:"last_seen_at"`
	// DisplayName is the name gs agent rename gave the daemon, if any. Name
	// stays the one it registers with (gs agent start --name).
	DisplayName string `json:"display_name,omitempty"`
	Account     string `json:"account,omitempty"`
	// Mine is true for the daemons this identity registered. The others
	// belong to agents whose account it administers.
	Mine bool `json:"mine"`
	// CISlices are the slices that use the daemon as their CI daemon.
	CISlices []string `json:"ci_slices,omitempty"`
	// Manageable is true when you may rename the daemon and choose which
	// slices it is exposed to; ExposedTo lists those slices.
	Manageable bool     `json:"manageable"`
	ExposedTo  []string `json:"exposed_to,omitempty"`
}

// shownName is the name a daemon is shown by.
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
	start := agentStartOptions{Runtime: defaultAgentRuntime}
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Run and inspect local BYOA agent daemons",
		RunE:  requireSubcommand("agent"),
	}
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start a local agent daemon",
		Args:  noArgs("gs agent start [--name NAME] [--runtime codex] [--ci]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			start.RuntimeSet = cmd.Flags().Changed("runtime")
			return r.runAgentStart(cmd.Context(), *opts, start)
		},
	}
	startCmd.Flags().StringVar(&start.Name, "name", start.Name, "name the daemon registers with; starting again with the same name finds the same daemon, with the slices that use it and the name gs agent rename gave it")
	startCmd.Flags().StringVar(&start.Runtime, "runtime", start.Runtime, "agent runtime")
	startCmd.Flags().DurationVar(&start.IdleTimeout, "idle-timeout", start.IdleTimeout, "disconnect and exit after this much conversation inactivity (0 = never)")
	startCmd.Flags().BoolVar(&start.Lazy, "lazy", start.Lazy, "mount each conversation's workspace lazily (gs mount) instead of checking it out; falls back to a checkout without FUSE")
	startCmd.Flags().BoolVar(&start.CI, "ci", false, "run checks only: no conversations, no agent runtime, any directory")

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

	agentCmd.AddCommand(startCmd, statusCmd, stopCmd, renameCmd, exposeCmd, unexposeCmd, r.agentDoctorCommand(opts), r.agentInstallCommand(opts), r.agentUninstallCommand(opts))
	return agentCmd
}

func (r Runner) runAgentStart(ctx context.Context, opts commandOptions, in agentStartOptions) error {
	if in.CI && in.Lazy {
		return userError("invalid_args", "--ci cannot be used with --lazy", "Run checks-only daemons without --lazy.")
	}
	if in.CI && in.RuntimeSet {
		return userError("invalid_args", "--ci cannot be used with --runtime", "Checks-only daemons always register runtime ci.")
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

	runtimeName := ciAgentRuntime
	var runtime agentRuntime
	if !in.CI {
		runtimeName = strings.TrimSpace(in.Runtime)
		if runtimeName == "" {
			runtimeName = defaultAgentRuntime
		}
		runtime, err = agentRuntimeForName(runtimeName)
		if err != nil {
			return err
		}
	}

	root := r.cwd()
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}

	signalCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	agentCtx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	daemon := &agentDaemon{
		runner:          r,
		cfg:             cfg,
		runtime:         runtime,
		baseCtx:         agentCtx,
		workingDir:      root,
		conversations:   map[string]*agentConversation{},
		checkRuns:       map[string]context.CancelFunc{},
		recentCheckRuns: map[string]struct{}{},
		checkSlots:      checkSlotsFromEnv(),
		lazy:            in.Lazy,
	}
	if !in.CI {
		daemon.reattachLazyWorkspaces(agentCtx)
		daemon.loadExistingConversations()
	}
	daemon.lastActivity = time.Now()
	if idleTimeout > 0 {
		go daemon.runIdleMonitor(agentCtx, cancel, idleTimeout)
	}
	defer func() {
		daemon.cancelAll()
		cancel()
	}()
	reportIdleExit := func() {
		if daemon.idleShutdownRequested() && !opts.Quiet {
			fmt.Fprintln(r.stderr(), "agent daemon exited after idle timeout")
		}
	}

	reportedOnline := false
	for {
		err := daemon.serveAgentConnection(agentCtx, opts, in.Name, runtimeName, !reportedOnline)
		if err == nil {
			if agentCtx.Err() != nil {
				reportIdleExit()
			}
			return nil
		}
		if agentCtx.Err() != nil {
			reportIdleExit()
			return nil
		}
		reportedOnline = true
		if !opts.Quiet {
			fmt.Fprintf(r.stderr(), "agent stream disconnected: %v; reconnecting...\n", err)
		}
		select {
		case <-agentCtx.Done():
			reportIdleExit()
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *agentDaemon) serveAgentConnection(ctx context.Context, opts commandOptions, name, runtimeName string, reportOnline bool) error {
	conn, err := dialAgentStream(ctx, d.cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	client := corev1.NewAgentServiceClient(conn)
	stream, err := client.Connect(authContext(connCtx, d.cfg))
	if err != nil {
		return err
	}

	sendQueue := newAgentSendQueue(connCtx, stream, agentSendBuffer)
	d.mu.Lock()
	d.sendQueue = sendQueue
	d.mu.Unlock()
	defer func() {
		cancel()
		_ = sendQueue.wait()
		d.mu.Lock()
		if d.sendQueue == sendQueue {
			d.sendQueue = nil
		}
		d.mu.Unlock()
	}()

	if err := sendQueue.send(connCtx, registerDaemonMessage(name, runtimeName)); err != nil {
		return err
	}
	registered, err := stream.Recv()
	if err != nil {
		return err
	}
	ack := registered.GetRegistered()
	if ack == nil || strings.TrimSpace(ack.GetDaemonId()) == "" {
		return userError("agent_protocol_error", "server did not acknowledge agent daemon registration", "Try restarting the daemon.")
	}
	lastInbound := newAgentInboundState(time.Now())
	if reportOnline && opts.jsonOutput() {
		if err := d.runner.writeJSONOutput(opts, map[string]string{
			"daemon_id": ack.GetDaemonId(),
			"status":    "online",
		}); err != nil {
			return err
		}
	} else if reportOnline && !opts.Quiet {
		fmt.Fprintf(d.runner.Stdout, "agent daemon %s online\n", ack.GetDaemonId())
	}

	// Resend any events buffered while disconnected over the fresh stream. The
	// server dedups by (conversation_id, client_seq), so replaying already-
	// persisted events is idempotent.
	go d.resendPendingAll()
	go d.runHeartbeat(connCtx, cancel)
	go d.runInboundWatchdog(connCtx, cancel, lastInbound)

	for {
		msg, err := stream.Recv()
		if err != nil {
			cancel()
			if agentStreamEndedCleanly(ctx, err) {
				return nil
			}
			if queueErr := sendQueue.err(); queueErr != nil && !errors.Is(queueErr, context.Canceled) {
				return queueErr
			}
			return err
		}
		lastInbound.mark(time.Now())
		d.handleServerMessage(connCtx, cancel, msg)
	}
}

func (r Runner) runAgentStatus(ctx context.Context, opts commandOptions, forSlice string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	callCtx := authContext(ctx, cfg)
	request := &corev1.ListDaemonsRequest{}
	if strings.TrimSpace(forSlice) != "" {
		if request.ForSlice, err = r.resolveSliceRefInput(callCtx, cfg, conn, forSlice); err != nil {
			return err
		}
	}
	res, err := corev1.NewAgentServiceClient(conn).ListDaemons(callCtx, request)
	if err != nil {
		return err
	}
	out := agentStatusOutput{Daemons: make([]agentDaemonOutput, 0, len(res.GetDaemons()))}
	for _, daemon := range res.GetDaemons() {
		out.Daemons = append(out.Daemons, agentDaemonToOutput(daemon))
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if len(out.Daemons) == 0 {
		fmt.Fprintln(r.Stdout, "no agent daemons")
		return nil
	}
	w := tabwriter.NewWriter(r.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tACCOUNT\tRUNTIME\tSTATUS\tLAST_SEEN\tCI_FOR\tEXPOSED_TO")
	for _, daemon := range out.Daemons {
		name := daemon.shownName()
		if daemon.DisplayName != "" && daemon.DisplayName != daemon.Name {
			name += " (" + daemon.Name + ")"
		}
		exposed := firstNonEmpty(strings.Join(daemon.ExposedTo, ", "), "-")
		if !daemon.Manageable {
			exposed = "(exposed to you)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", daemon.ID, name, daemon.Account, daemon.Runtime, daemon.Status, daemon.LastSeenAt, firstNonEmpty(strings.Join(daemon.CISlices, ", "), "-"), exposed)
	}
	return w.Flush()
}

// runAgentRename sets, or with an empty name clears, the name a daemon is
// shown by.
func (r Runner) runAgentRename(ctx context.Context, opts commandOptions, selector, newName string) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewAgentServiceClient(conn)
	callCtx := authContext(ctx, cfg)
	res, err := client.ListDaemons(callCtx, &corev1.ListDaemonsRequest{})
	if err != nil {
		return err
	}
	daemon, err := pickDaemon(res.GetDaemons(), selector)
	if err != nil {
		return err
	}
	renamed, err := client.RenameDaemon(callCtx, &corev1.RenameDaemonRequest{DaemonId: daemon.GetId(), DisplayName: newName})
	if err != nil {
		if grpcstatus.Code(err) == codes.AlreadyExists {
			return userError("name_taken", grpcstatus.Convert(err).Message(), "Pick another name; gs agent status lists the account's daemons.")
		}
		return err
	}
	out := agentDaemonToOutput(renamed)
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	if renamed.GetDisplayName() == "" {
		fmt.Fprintf(r.Stdout, "%s is shown as %s again\n", renamed.GetId(), renamed.GetName())
		return nil
	}
	fmt.Fprintf(r.Stdout, "%s is now shown as %s (it still registers as %s)\n", renamed.GetId(), renamed.GetDisplayName(), renamed.GetName())
	return nil
}

// runAgentExpose exposes a daemon to a slice, or with expose false withdraws it.
func (r Runner) runAgentExpose(ctx context.Context, opts commandOptions, selector, sliceArg string, expose bool) error {
	cfg, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := corev1.NewAgentServiceClient(conn)
	res, err := client.ListDaemons(callCtx, &corev1.ListDaemonsRequest{})
	if err != nil {
		return err
	}
	daemon, err := pickDaemon(res.GetDaemons(), selector)
	if err != nil {
		return err
	}
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceArg)
	if err != nil {
		return err
	}
	var updated *corev1.AgentDaemon
	if expose {
		updated, err = client.ExposeDaemon(callCtx, &corev1.ExposeDaemonRequest{DaemonId: daemon.GetId(), Slice: ref})
	} else {
		updated, err = client.UnexposeDaemon(callCtx, &corev1.UnexposeDaemonRequest{DaemonId: daemon.GetId(), Slice: ref})
	}
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, agentDaemonToOutput(updated))
	}
	if opts.Quiet {
		return nil
	}
	name := firstNonEmpty(updated.GetDisplayName(), updated.GetName())
	if expose {
		fmt.Fprintf(r.Stdout, "%s (%s) is exposed to %s: its owners and admins can choose it with gs slice set-ci-daemon\n", name, updated.GetId(), sliceRefLabel(ref))
		return nil
	}
	fmt.Fprintf(r.Stdout, "%s (%s) is no longer exposed to %s\n", name, updated.GetId(), sliceRefLabel(ref))
	if sliceCI := updated.GetCiSlices(); len(sliceCI) > 0 {
		for _, used := range sliceCI {
			if used.GetAccount() == ref.GetAccount() && used.GetSlice() == ref.GetSlice() {
				fmt.Fprintf(r.Stdout, "that slice still names it, so its checks are refused until it names another daemon\n")
			}
		}
	}
	return nil
}

// pickDaemon finds the daemon a user named: by id, else by the name it is
// shown by or the one it registered with.
func pickDaemon(daemons []*corev1.AgentDaemon, selector string) (*corev1.AgentDaemon, error) {
	selector = strings.TrimSpace(selector)
	for _, d := range daemons {
		if d.GetId() == selector {
			return d, nil
		}
	}
	var matches []*corev1.AgentDaemon
	for _, d := range daemons {
		if strings.EqualFold(d.GetDisplayName(), selector) || strings.EqualFold(d.GetName(), selector) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, userError("daemon_not_found", "no agent daemon "+selector, "gs agent status lists the daemons you can rename; use the id from its first column.")
	}
	ids := make([]string, len(matches))
	for i, d := range matches {
		ids[i] = d.GetId() + " (" + d.GetAccount() + ")"
	}
	return nil, userError("ambiguous_daemon", selector+" names more than one daemon: "+strings.Join(ids, ", "), "Use the daemon's id.")
}

func (r Runner) runAgentStop(opts commandOptions) error {
	out := map[string]string{
		"status": "manual",
		"hint":   "agent daemons stop when their gs agent start process receives Ctrl-C; installed daemons stop with gs agent uninstall <name>",
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintln(r.Stdout, "agent daemons stop when their gs agent start process receives Ctrl-C; installed daemons stop with gs agent uninstall <name>")
	return nil
}

type agentDaemon struct {
	runner     Runner
	cfg        UserConfig
	runtime    agentRuntime
	baseCtx    context.Context
	workingDir string
	sendQueue  *agentSendQueue

	idleMu       sync.Mutex
	lastActivity time.Time
	activeWork   int
	idleShutdown bool

	// checkSlots bounds how many checks run at once
	// (GITSLICE_CHECK_CONCURRENCY); nil runs every dispatched check at once.
	checkSlots chan struct{}

	// lazy mounts new conversation workspaces instead of checking them out.
	lazy bool

	mu                  sync.Mutex
	conversations       map[string]*agentConversation
	checkRuns           map[string]context.CancelFunc
	checkRunOutboxes    map[string]*checkRunOutbox
	checkRunOutboxOrder []string
	recentCheckRuns     map[string]struct{}
	recentCheckRunOrder []string
}

func (d *agentDaemon) cancelAll() {
	d.mu.Lock()
	conversations := make([]*agentConversation, 0, len(d.conversations))
	for _, conv := range d.conversations {
		conversations = append(conversations, conv)
	}
	d.mu.Unlock()
	for _, conv := range conversations {
		conv.cancelRun()
		conv.closeSession()
	}
}

func agentRuntimeForName(name string) (agentRuntime, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", defaultAgentRuntime:
		return codexRuntime{}, nil
	default:
		return nil, userError("invalid_agent_runtime", "unsupported agent runtime: "+name, "Only codex is supported in this version.")
	}
}

// checkSlotsFromEnv reads GITSLICE_CHECK_CONCURRENCY, the most checks the
// daemon runs at once. Unset or not a positive number means no limit; a small
// CI host sets 1 so timing-sensitive test suites do not compete for CPU.
func checkSlotsFromEnv() chan struct{} {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("GITSLICE_CHECK_CONCURRENCY")))
	if err != nil || n <= 0 {
		return nil
	}
	return make(chan struct{}, n)
}

func defaultAgentName() string {
	hostname, err := os.Hostname()
	if err == nil && strings.TrimSpace(hostname) != "" {
		return hostname
	}
	return "gs-agent"
}

func agentConversationPaths(root, conversationID string) (string, string, error) {
	if strings.TrimSpace(conversationID) == "" {
		return "", "", userError("invalid_conversation_id", "agent conversation id is empty", "Try creating a new conversation.")
	}
	if filepath.IsAbs(conversationID) || conversationID == "." || conversationID == ".." || strings.ContainsAny(conversationID, `/\`) {
		return "", "", userError("invalid_conversation_id", "invalid agent conversation id: "+conversationID, "Try creating a new conversation.")
	}
	subdir := filepath.ToSlash(filepath.Join("conversations", conversationID))
	return subdir, filepath.Join(root, "conversations", conversationID), nil
}

func agentWorkspaceInitEnv(r Runner, cfg UserConfig) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, "GS_SERVER_ADDR="+cfg.ServerAddr)
	env = append(env, "GITSLICE_SERVER_ADDR="+cfg.ServerAddr)
	if r.Home != "" {
		env = append(env, "HOME="+r.Home)
	}
	return env
}

func agentStreamEndedCleanly(ctx context.Context, err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	return ctx.Err() != nil
}
