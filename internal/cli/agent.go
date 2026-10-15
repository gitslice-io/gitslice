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
)

const (
	defaultAgentRuntime = "codex"
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
	IdleTimeout time.Duration
}

type agentDaemonOutput struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Runtime    string `json:"runtime"`
	Status     string `json:"status"`
	LastSeenAt string `json:"last_seen_at"`
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
		Args:  noArgs("gs agent start [--name NAME] [--runtime codex]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAgentStart(cmd.Context(), *opts, start)
		},
	}
	startCmd.Flags().StringVar(&start.Name, "name", start.Name, "daemon name")
	startCmd.Flags().StringVar(&start.Runtime, "runtime", start.Runtime, "agent runtime")
	startCmd.Flags().DurationVar(&start.IdleTimeout, "idle-timeout", start.IdleTimeout, "disconnect and exit after this much conversation inactivity (0 = never)")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "List agent daemons for the signed-in account",
		Args:  noArgs("gs agent status [--format text|json] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAgentStatus(cmd.Context(), *opts)
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

	agentCmd.AddCommand(startCmd, statusCmd, stopCmd)
	return agentCmd
}

func (r Runner) runAgentStart(ctx context.Context, opts commandOptions, in agentStartOptions) error {
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
	if err := r.requireAgentWorkspaceDir(); err != nil {
		return err
	}

	runtimeName := strings.TrimSpace(in.Runtime)
	if runtimeName == "" {
		runtimeName = defaultAgentRuntime
	}
	runtime, err := agentRuntimeForName(runtimeName)
	if err != nil {
		return err
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
	}
	daemon.loadExistingConversations()
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

func (r Runner) runAgentStatus(ctx context.Context, opts commandOptions) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	conn, err := dial(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	res, err := corev1.NewAgentServiceClient(conn).ListDaemons(authContext(ctx, cfg), &corev1.ListDaemonsRequest{})
	if err != nil {
		return err
	}
	out := agentStatusOutput{Daemons: make([]agentDaemonOutput, 0, len(res.GetDaemons()))}
	for _, daemon := range res.GetDaemons() {
		out.Daemons = append(out.Daemons, agentDaemonOutput{
			ID:         daemon.GetId(),
			Name:       daemon.GetName(),
			Runtime:    daemon.GetRuntime(),
			Status:     daemon.GetStatus(),
			LastSeenAt: daemon.GetLastSeenAt(),
		})
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
	fmt.Fprintln(w, "ID\tNAME\tRUNTIME\tSTATUS\tLAST_SEEN")
	for _, daemon := range out.Daemons {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", daemon.ID, daemon.Name, daemon.Runtime, daemon.Status, daemon.LastSeenAt)
	}
	return w.Flush()
}

func (r Runner) runAgentStop(opts commandOptions) error {
	out := map[string]string{
		"status": "manual",
		"hint":   "agent daemons stop when their gs agent start process receives Ctrl-C",
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintln(r.Stdout, "agent daemons stop when their gs agent start process receives Ctrl-C")
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
