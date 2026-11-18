package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/agentd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

const (
	defaultAgentRuntime            = agentd.DefaultRuntimeName
	ciAgentRuntime                 = agentd.CIRuntimeName
	agentSendBuffer                = agentd.SendBuffer
	agentHeartbeat                 = agentd.HeartbeatInterval
	agentInboundStaleTimeout       = agentd.InboundStaleTimeout
	agentInboundStaleCheckInterval = agentd.InboundStaleCheckEvery
	agentMetaFilename              = agentd.MetaFilename
	agentRecentCheckRunLimit       = agentd.RecentCheckRunLimit
	agentMaxPendingEvents          = agentd.MaxPendingEvents
	agentTurnCompleteType          = agentd.TurnCompleteType
)

var (
	checkSlotsFromEnv      = agentd.CheckSlotsFromEnv
	newAgentInstanceID     = agentd.NewInstanceID
	defaultAgentName       = agentd.DefaultAgentName
	agentConversationPaths = agentd.ConversationPaths
	canonicalSliceKey      = agentd.CanonicalSliceKey
)

type agentStartOptions struct {
	Name        string
	Runtime     string
	RuntimeSet  bool
	CI          bool
	Slices      []string
	IdleTimeout time.Duration
	// Lazy mounts each conversation's workspace (gs mount) instead of
	// checking it out, where FUSE is available.
	Lazy bool
	// DetachAfter is how long a checks-only daemon stays connected with
	// nothing to run before it detaches and polls; DetachSet says it was
	// given. See agent_standby.go.
	DetachAfter time.Duration
	DetachSet   bool
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
	startCmd.Flags().DurationVar(&start.DetachAfter, "detach-after", defaultDetachAfter, "checks-only daemons (--ci): detach from the server after this long with nothing to run, and poll for queued checks (0 = stay connected; env GITSLICE_AGENT_DETACH_AFTER). The server then has no stream to hold open and can scale to zero")
	startCmd.Flags().BoolVar(&start.CI, "ci", false, "run checks only: no agent runtime, any directory")
	// Flags for hosting agent conversations, which are not offered for now:
	// they keep working, but help does not list them.
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

	detachAfter, err := detachAfterFor(in)
	if err != nil {
		return err
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
		name:            in.Name,
		allowedSlices:   allowedSlices,
		instanceID:      newAgentInstanceID(),
		detachAfter:     detachAfter,
		quiet:           opts.Quiet,
	}
	defer daemon.closePollConnection()
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
		if errors.Is(err, errAgentDetached) {
			reportedOnline = true
			// Work that arrived as the stream closed stays here to be done:
			// reconnect instead of waiting.
			if daemon.checksIdle() {
				if standbyErr := daemon.standby(agentCtx); standbyErr != nil {
					reportIdleExit()
					return nil
				}
			}
			continue
		}
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
		_ = sendQueue.Wait()
		d.mu.Lock()
		if d.sendQueue == sendQueue {
			d.sendQueue = nil
		}
		d.mu.Unlock()
	}()

	if err := sendQueue.Send(connCtx, registerDaemonMessage(name, runtimeName, d.instanceID, cap(d.checkSlots))); err != nil {
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
	d.setDaemonID(ack.GetDaemonId())
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
	if d.detachAfter > 0 && !d.standbyUnsupported.Load() {
		go d.runDetachWatch(connCtx)
	}
	go d.runInboundWatchdog(connCtx, cancel, lastInbound)

	for {
		msg, err := stream.Recv()
		if err != nil {
			cancel()
			if d.detaching.Swap(false) && errors.Is(err, io.EOF) {
				return errAgentDetached
			}
			if agentStreamEndedCleanly(ctx, err) {
				return nil
			}
			if queueErr := sendQueue.Err(); queueErr != nil && !errors.Is(queueErr, context.Canceled) {
				return queueErr
			}
			return err
		}
		lastInbound.Mark(time.Now())
		d.handleServerMessage(connCtx, cancel, msg)
	}
}

type agentAuthClient interface {
	CreateApiKey(ctx context.Context, in *corev1.CreateApiKeyRequest, opts ...grpc.CallOption) (*corev1.CreateApiKeyResponse, error)
	RevokeApiKey(ctx context.Context, in *corev1.RevokeApiKeyRequest, opts ...grpc.CallOption) (*corev1.RevokeApiKeyResponse, error)
}

type agentDaemon struct {
	runner        Runner
	cfg           UserConfig
	auth          agentAuthClient // optional override for tests; nil dials cfg.ServerAddr
	runtime       agentRuntime
	baseCtx       context.Context
	workingDir    string
	sendQueue     *agentSendQueue
	name          string   // the name it registers with, as given
	daemonID      string   // registered daemon id from server ack; guarded by mu
	allowedSlices []string // optional canonical <account>/<slice> allowlist from --slice
	// instanceID names this process to the server, which sends each check
	// run to one process of a daemon (design/32 layer 3).
	instanceID string

	// Detaching (agent_standby.go): a checks-only daemon with nothing to run
	// closes its stream and polls instead.
	detachAfter        time.Duration
	detaching          atomic.Bool // a detach was sent; the stream is about to end
	standbyUnsupported atomic.Bool // the server cannot take a detached daemon
	doorbellOpen       atomic.Bool // its next standby poll says the doorbell socket is open
	standbyPollFn      func(context.Context) (*corev1.StandbyPollResponse, error)
	pollMu             sync.Mutex
	pollConn           *grpc.ClientConn
	quiet              bool

	idleMu       sync.Mutex
	lastActivity time.Time
	activeWork   int
	idleShutdown bool

	// checkSlots bounds how many checks run at once
	// (GITSLICE_CHECK_CONCURRENCY); nil runs every dispatched check at once.
	checkSlots chan struct{}

	// lazy mounts new conversation workspaces instead of checking them out.
	lazy bool

	// Optional hooks for isolated lazy-workspace tests.
	isMountedFn func(dir string) bool
	runGSFn     func(ctx context.Context, dir string, envOverrides map[string]string, args ...string) (string, error)
	unmountFn   func(ctx context.Context, conversationID string, rec mountRecord) error

	mu                  sync.Mutex
	conversations       map[string]*agentConversation
	reattachedKeys      map[string]reattachedConversationKey
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

func (d *agentDaemon) setDaemonID(id string) {
	id = strings.TrimSpace(id)
	d.mu.Lock()
	d.daemonID = id
	d.mu.Unlock()
}

func (d *agentDaemon) currentDaemonID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.daemonID
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
		key := canonicalSliceKey(ref.GetAccount(), ref.GetSlice())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out, nil
}

func (d *agentDaemon) allowsSlice(ref *corev1.SliceRef) bool {
	if len(d.allowedSlices) == 0 {
		return true
	}
	key := canonicalSliceKey(ref.GetAccount(), ref.GetSlice())
	return key != "" && slices.Contains(d.allowedSlices, key)
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

func agentWorkspaceInitEnvWithOverrides(r Runner, cfg UserConfig, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return agentWorkspaceInitEnv(r, cfg)
	}
	return mergeAgentEnv(sanitizedAgentRuntimeEnv(agentWorkspaceInitEnv(r, cfg)), overrides)
}

func agentStreamEndedCleanly(ctx context.Context, err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	return ctx.Err() != nil
}
