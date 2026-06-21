package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/spf13/cobra"
)

const (
	defaultAgentRuntime = "codex"
	agentSendBuffer     = 128
	agentHeartbeat      = 15 * time.Second
)

type agentStartOptions struct {
	Name    string
	Runtime string
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
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	if err := r.requireEmptyWorkspaceInitDir(); err != nil {
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
		runner:        r,
		cfg:           cfg,
		runtime:       runtime,
		workingDir:    root,
		conversations: map[string]*agentConversation{},
	}
	defer func() {
		daemon.cancelAll()
		cancel()
	}()

	reportedOnline := false
	for {
		err := daemon.serveAgentConnection(agentCtx, opts, in.Name, runtimeName, !reportedOnline)
		if err == nil {
			return nil
		}
		if agentCtx.Err() != nil {
			return nil
		}
		reportedOnline = true
		if !opts.Quiet {
			fmt.Fprintf(r.stderr(), "agent stream disconnected: %v; reconnecting...\n", err)
		}
		select {
		case <-agentCtx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *agentDaemon) serveAgentConnection(ctx context.Context, opts commandOptions, name, runtimeName string, reportOnline bool) error {
	conn, err := dial(ctx, d.cfg.ServerAddr)
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

	go d.runHeartbeat(connCtx, cancel)

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
	workingDir string
	sendQueue  *agentSendQueue

	mu            sync.Mutex
	conversations map[string]*agentConversation
}

type agentConversation struct {
	id              string
	title           string
	workdir         string
	workspaceSubdir string
	ready           chan struct{}
	readyOnce       sync.Once
	readyErr        error

	runMu   sync.Mutex
	stateMu sync.Mutex
	cancel  context.CancelFunc
}

func (d *agentDaemon) handleServerMessage(ctx context.Context, cancel context.CancelFunc, msg *corev1.ServerMessage) {
	switch payload := msg.GetPayload().(type) {
	case *corev1.ServerMessage_Start:
		go d.handleStartConversation(ctx, payload.Start)
	case *corev1.ServerMessage_UserMessage:
		go d.handleUserMessage(ctx, payload.UserMessage)
	case *corev1.ServerMessage_Cancel:
		d.handleCancelConversation(payload.Cancel)
	case *corev1.ServerMessage_Ping:
		if err := d.sendDaemonMessage(ctx, heartbeatDaemonMessage()); err != nil {
			cancel()
		}
	}
}

func (d *agentDaemon) runHeartbeat(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(agentHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.sendDaemonMessage(ctx, heartbeatDaemonMessage()); err != nil {
				cancel()
				return
			}
		}
	}
}

func (d *agentDaemon) handleStartConversation(ctx context.Context, start *corev1.StartConversation) {
	conversationID := strings.TrimSpace(start.GetConversationId())
	conv, err := d.createConversation(conversationID)
	if err != nil {
		d.sendSystemError(ctx, conversationID, err)
		return
	}
	conv.title = strings.TrimSpace(start.GetTitle())

	slice := start.GetSlice()
	if slice == nil || strings.TrimSpace(slice.GetAccount()) == "" || strings.TrimSpace(slice.GetSlice()) == "" {
		err := userError("invalid_agent_conversation", "server started an agent conversation without a valid slice", "Try creating a new conversation.")
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}
	if err := d.hydrateWorkspace(ctx, conv, sliceRefLabel(slice)); err != nil {
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}

	conv.setReady(nil)
	_ = d.sendQueue.send(ctx, &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
			ConversationId:  conversationID,
			WorkspaceSubdir: conv.workspaceSubdir,
		}},
	})
}

func (d *agentDaemon) handleUserMessage(ctx context.Context, msg *corev1.DeliverUserMessage) {
	conversationID := strings.TrimSpace(msg.GetConversationId())
	conv := d.getConversation(conversationID)
	if conv == nil {
		d.sendSystemError(ctx, conversationID, fmt.Errorf("agent conversation is not ready: %s", conversationID))
		return
	}
	if err := conv.waitReady(ctx); err != nil {
		d.sendSystemError(ctx, conversationID, err)
		return
	}

	conv.runMu.Lock()
	defer conv.runMu.Unlock()
	if err := ctx.Err(); err != nil {
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	conv.setCancel(cancel)
	defer conv.clearCancel(cancel)
	defer cancel()

	err := forwardAgentRuntime(runCtx, d.runtime, conv.workdir, conversationID, msg.GetText(), func(event *corev1.AgentEvent) {
		_ = d.sendAgentEvent(runCtx, event)
	})
	if errors.Is(err, context.Canceled) {
		if ctx.Err() == nil {
			_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
				ConversationId: conversationID,
				Role:           "agent",
				Type:           "status",
				Text:           "canceled",
				Final:          true,
			})
		}
		return
	}
	if err != nil {
		d.sendSystemError(ctx, conversationID, fmt.Errorf("agent runtime failed: %w", err))
		return
	}

	// Capture any edits the turn produced as a (conversation-linked) patchset.
	if ctx.Err() == nil {
		d.capturePatchset(ctx, conv)
	}
}

// capturePatchset records the workspace edits produced by the latest turn as a
// patchset linked to this conversation. It is best-effort: `gs cs capture` is a
// no-op when there are no edits, and any failure is surfaced as a status event
// rather than failing the turn.
func (d *agentDaemon) capturePatchset(ctx context.Context, conv *agentConversation) {
	title := conv.title
	if title == "" {
		title = "agent: " + conv.id
	}
	out, err := d.runWorkspaceGS(ctx, conv, "cs", "capture", "--title", title)
	if err != nil {
		text := "patchset capture failed"
		if out != "" {
			text = "patchset capture failed: " + out
		}
		_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
			ConversationId: conv.id,
			Role:           "system",
			Type:           "error",
			Text:           text,
		})
		return
	}
	if out != "" && out != "no changes to capture" {
		_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
			ConversationId: conv.id,
			Role:           "system",
			Type:           "status",
			Text:           out,
		})
	}
}

// runWorkspaceGS runs the gs binary against this conversation's workspace subdir,
// inheriting the daemon's auth/server environment, and returns trimmed combined
// output.
func (d *agentDaemon) runWorkspaceGS(ctx context.Context, conv *agentConversation, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = conv.workdir
	cmd.Env = agentWorkspaceInitEnv(d.runner, d.cfg)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (d *agentDaemon) handleCancelConversation(cancel *corev1.CancelConversation) {
	conv := d.getConversation(strings.TrimSpace(cancel.GetConversationId()))
	if conv == nil {
		return
	}
	conv.cancelRun()
}

func (d *agentDaemon) createConversation(conversationID string) (*agentConversation, error) {
	subdir, workdir, err := agentConversationPaths(d.workingDir, conversationID)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing := d.conversations[conversationID]; existing != nil {
		return existing, nil
	}
	conv := &agentConversation{
		id:              conversationID,
		workdir:         workdir,
		workspaceSubdir: subdir,
		ready:           make(chan struct{}),
	}
	d.conversations[conversationID] = conv
	return conv, nil
}

func (d *agentDaemon) getConversation(conversationID string) *agentConversation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conversations[conversationID]
}

func (d *agentDaemon) hydrateWorkspace(ctx context.Context, conv *agentConversation, sliceRef string) error {
	if err := os.MkdirAll(conv.workdir, 0o755); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "--quiet", "workspace", "init", sliceRef, "--agent-conversation", conv.id)
	cmd.Dir = conv.workdir
	cmd.Env = agentWorkspaceInitEnv(d.runner, d.cfg)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("workspace init failed for %s: %w", sliceRef, err)
	}
	return fmt.Errorf("workspace init failed for %s: %w: %s", sliceRef, err, detail)
}

func (d *agentDaemon) sendAgentEvent(ctx context.Context, event *corev1.AgentEvent) error {
	return d.sendDaemonMessage(ctx, &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_Event{Event: event},
	})
}

func (d *agentDaemon) sendDaemonMessage(ctx context.Context, msg *corev1.DaemonMessage) error {
	d.mu.Lock()
	sendQueue := d.sendQueue
	d.mu.Unlock()
	if sendQueue == nil {
		return context.Canceled
	}
	return sendQueue.send(ctx, msg)
}

func (d *agentDaemon) sendSystemError(ctx context.Context, conversationID string, err error) {
	if strings.TrimSpace(conversationID) == "" {
		return
	}
	_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
		ConversationId: conversationID,
		Role:           "system",
		Type:           "error",
		Text:           err.Error(),
		Final:          true,
	})
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
	}
}

func (c *agentConversation) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		return c.readyErr
	}
}

func (c *agentConversation) setReady(err error) {
	c.readyOnce.Do(func() {
		c.stateMu.Lock()
		c.readyErr = err
		c.stateMu.Unlock()
		close(c.ready)
	})
}

func (c *agentConversation) setCancel(cancel context.CancelFunc) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.cancel = cancel
}

func (c *agentConversation) clearCancel(cancel context.CancelFunc) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.cancel = nil
}

func (c *agentConversation) cancelRun() {
	c.stateMu.Lock()
	cancel := c.cancel
	c.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type agentSendQueue struct {
	ch   chan *corev1.DaemonMessage
	done chan struct{}

	mu      sync.Mutex
	sendErr error
}

func newAgentSendQueue(ctx context.Context, stream corev1.AgentService_ConnectClient, buffer int) *agentSendQueue {
	q := &agentSendQueue{
		ch:   make(chan *corev1.DaemonMessage, buffer),
		done: make(chan struct{}),
	}
	go func() {
		defer close(q.done)
		for {
			select {
			case <-ctx.Done():
				_ = stream.CloseSend()
				q.setErr(ctx.Err())
				return
			case msg := <-q.ch:
				if msg == nil {
					continue
				}
				if err := stream.Send(msg); err != nil {
					q.setErr(err)
					return
				}
			}
		}
	}()
	return q
}

func (q *agentSendQueue) send(ctx context.Context, msg *corev1.DaemonMessage) error {
	select {
	case <-q.done:
		if err := q.err(); err != nil {
			return err
		}
		return io.ErrClosedPipe
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-q.done:
		if err := q.err(); err != nil {
			return err
		}
		return io.ErrClosedPipe
	case q.ch <- msg:
		return nil
	}
}

func (q *agentSendQueue) wait() error {
	<-q.done
	return q.err()
}

func (q *agentSendQueue) setErr(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sendErr = err
}

func (q *agentSendQueue) err() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.sendErr
}

func forwardAgentRuntime(ctx context.Context, runtime agentRuntime, workdir, conversationID, prompt string, emitEvent func(*corev1.AgentEvent)) error {
	return runtime.Run(ctx, workdir, prompt, func(event agentRuntimeEvent) {
		role := event.Role
		if role == "" {
			role = "agent"
		}
		eventType := event.Type
		if eventType == "" {
			eventType = "delta"
		}
		emitEvent(&corev1.AgentEvent{
			ConversationId: conversationID,
			Role:           role,
			Type:           eventType,
			Text:           event.Text,
			DataJson:       event.Data,
			Final:          event.Final,
		})
	})
}

func registerDaemonMessage(name, runtimeName string) *corev1.DaemonMessage {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultAgentName()
	}
	version := cliVersionInfo().Version
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
			Name:    name,
			Runtime: runtimeName,
			Version: version,
		}},
	}
}

func heartbeatDaemonMessage() *corev1.DaemonMessage {
	return &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_Heartbeat{Heartbeat: &corev1.Heartbeat{}},
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
