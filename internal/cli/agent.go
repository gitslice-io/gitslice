package cli

import (
	"context"
	"encoding/json"
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
	}
	daemon.loadExistingConversations()
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

	mu                  sync.Mutex
	conversations       map[string]*agentConversation
	checkRuns           map[string]context.CancelFunc
	checkRunOutboxes    map[string]*checkRunOutbox
	checkRunOutboxOrder []string
	recentCheckRuns     map[string]struct{}
	recentCheckRunOrder []string
}

type agentInboundState struct {
	mu   sync.Mutex
	last time.Time
}

func newAgentInboundState(now time.Time) *agentInboundState {
	return &agentInboundState{last: now}
}

func (s *agentInboundState) mark(now time.Time) {
	s.mu.Lock()
	s.last = now
	s.mu.Unlock()
}

func (s *agentInboundState) lastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

type agentConversation struct {
	id              string
	workdir         string
	workspaceSubdir string
	ready           chan struct{}
	readyOnce       sync.Once
	readyErr        error

	runMu   sync.Mutex
	stateMu sync.Mutex

	// outMu guards the outbound event buffer. Turn output is assigned a
	// per-conversation client_seq and buffered here until the server acks it, so
	// the events survive a Connect reconnect and are resent (the server dedups by
	// client_seq). All sends for a conversation go through flushPendingLocked
	// under outMu, which keeps them strictly ordered even across a reconnect.
	outMu          sync.Mutex
	nextClientSeq  int64
	pending        []*corev1.AgentEvent // emitted, not yet acked; ascending client_seq
	ackedClientSeq int64
	// flushedSeq is the highest client_seq already handed to flushQueue, so steady
	// state sends each event once. When the live send queue changes (reconnect),
	// flushPendingLocked resets flushedSeq to ackedClientSeq and resends the rest.
	flushedSeq int64
	flushQueue *agentSendQueue

	title             string
	sliceAccount      string
	sliceName         string
	sliceID           string
	processedUserSeqs map[int64]struct{} // user-message seqs already run; guarded by stateMu
	cancel            context.CancelFunc
	codexThreadID     string // runtime session to resume across turns; guarded by stateMu
	session           agentSession
}

type conversationMeta struct {
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	ThreadID       string `json:"thread_id"`
	SliceAccount   string `json:"slice_account"`
	SliceName      string `json:"slice_name"`
	SliceID        string `json:"slice_id"`
}

func (d *agentDaemon) handleServerMessage(ctx context.Context, cancel context.CancelFunc, msg *corev1.ServerMessage) {
	switch payload := msg.GetPayload().(type) {
	case *corev1.ServerMessage_Start:
		go d.handleStartConversation(ctx, payload.Start)
	case *corev1.ServerMessage_UserMessage:
		go d.handleUserMessage(ctx, payload.UserMessage)
	case *corev1.ServerMessage_Cancel:
		d.handleCancelConversation(payload.Cancel)
	case *corev1.ServerMessage_Close:
		go d.handleCloseWorkspace(payload.Close)
	case *corev1.ServerMessage_Reconcile:
		go d.handleReconcileWorkspaces(payload.Reconcile)
	case *corev1.ServerMessage_Ping:
		if err := d.sendDaemonMessage(ctx, heartbeatDaemonMessage()); err != nil {
			cancel()
		}
	case *corev1.ServerMessage_Ack:
		d.handleEventAck(payload.Ack)
	case *corev1.ServerMessage_RunChecks:
		fmt.Fprintf(d.runner.stderr(), "agent received RunChecks: checks=%d run_ids=%v\n", len(payload.RunChecks.GetChecks()), runCheckIDs(payload.RunChecks))
		go d.handleRunChecks(payload.RunChecks)
	case *corev1.ServerMessage_CancelCheck:
		d.handleCancelCheckRun(payload.CancelCheck)
	case *corev1.ServerMessage_CheckAck:
		d.handleCheckRunAck(payload.CheckAck)
	}
}

func runCheckIDs(req *corev1.RunChecks) []string {
	if req == nil {
		return nil
	}
	ids := make([]string, 0, len(req.GetChecks()))
	for _, spec := range req.GetChecks() {
		id := strings.TrimSpace(spec.GetRunId())
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// emitConversationEvent assigns the next per-conversation client_seq, buffers
// the event for ack/resend, and best-effort flushes the pending buffer over the
// current send queue. Buffered events survive Connect reconnects: the server
// dedups by (conversation_id, client_seq), so re-sending unacked events is
// idempotent. This is the delivery path for durable turn output; transient
// ephemeral token deltas use the unsequenced sendAgentEvent path instead.
func (d *agentDaemon) emitConversationEvent(conv *agentConversation, event *corev1.AgentEvent) {
	conv.outMu.Lock()
	defer conv.outMu.Unlock()
	conv.nextClientSeq++
	event.ClientSeq = conv.nextClientSeq
	conv.pending = append(conv.pending, event)
	if len(conv.pending) > agentMaxPendingEvents {
		// Degenerate: the server has not acked for a very long time. Drop the
		// oldest so the daemon does not grow unbounded.
		fmt.Fprintf(d.runner.stderr(), "agent outbound buffer overflow for %s; dropping oldest event\n", conv.id)
		conv.pending = conv.pending[1:]
	}
	d.flushPendingLocked(conv)
}

// flushPendingLocked sends every buffered event in client_seq order over the
// current send queue. The caller must hold conv.outMu, which serializes sends
// for the conversation (so a reconnect resend cannot interleave with new
// emits). Best-effort: it stops at the first failure (nil queue or send error),
// leaving the rest buffered for the next flush or reconnect. Sends use a
// background context so a finished or cancelled turn still flushes; agentSendQueue
// returns promptly once its connection context is done.
func (d *agentDaemon) flushPendingLocked(conv *agentConversation) {
	d.mu.Lock()
	sq := d.sendQueue
	d.mu.Unlock()
	if sq == nil {
		return
	}
	// New connection epoch: resend everything still unacked over the fresh queue.
	if sq != conv.flushQueue {
		conv.flushQueue = sq
		conv.flushedSeq = conv.ackedClientSeq
	}
	for _, ev := range conv.pending {
		if ev.ClientSeq <= conv.flushedSeq {
			continue
		}
		msg := &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: ev}}
		if err := sq.send(context.Background(), msg); err != nil {
			return
		}
		conv.flushedSeq = ev.ClientSeq
	}
}

// handleEventAck drops events the server has durably persisted from the
// conversation's resend buffer.
func (d *agentDaemon) handleEventAck(ack *corev1.EventAck) {
	if ack == nil {
		return
	}
	conv := d.getConversation(strings.TrimSpace(ack.GetConversationId()))
	if conv == nil {
		return
	}
	conv.outMu.Lock()
	defer conv.outMu.Unlock()
	if ack.GetAckedClientSeq() > conv.ackedClientSeq {
		conv.ackedClientSeq = ack.GetAckedClientSeq()
	}
	kept := conv.pending[:0]
	for _, ev := range conv.pending {
		if ev.ClientSeq > conv.ackedClientSeq {
			kept = append(kept, ev)
		}
	}
	conv.pending = kept
}

// resendPendingAll re-flushes every active conversation's buffered events after
// a (re)connect, in client_seq order per conversation.
func (d *agentDaemon) resendPendingAll() {
	d.mu.Lock()
	convs := make([]*agentConversation, 0, len(d.conversations))
	for _, conv := range d.conversations {
		convs = append(convs, conv)
	}
	checkOutboxes := make([]*checkRunOutbox, 0, len(d.checkRunOutboxes))
	for _, outbox := range d.checkRunOutboxes {
		checkOutboxes = append(checkOutboxes, outbox)
	}
	d.mu.Unlock()
	for _, conv := range convs {
		conv.outMu.Lock()
		d.flushPendingLocked(conv)
		conv.outMu.Unlock()
	}
	for _, outbox := range checkOutboxes {
		outbox.outMu.Lock()
		d.flushCheckRunPendingLocked(outbox)
		outbox.outMu.Unlock()
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

func (d *agentDaemon) runInboundWatchdog(ctx context.Context, cancel context.CancelFunc, inbound *agentInboundState) {
	if inbound == nil {
		return
	}
	ticker := time.NewTicker(agentInboundStaleCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			last := inbound.lastSeen()
			now := time.Now()
			if inboundStale(last, now, agentInboundStaleTimeout) {
				fmt.Fprintf(d.runner.stderr(), "no server message in %s; assuming half-open stream, reconnecting\n", now.Sub(last).Round(time.Second))
				cancel()
				return
			}
		}
	}
}

func inboundStale(last time.Time, now time.Time, timeout time.Duration) bool {
	if last.IsZero() || timeout <= 0 || now.Before(last) {
		return false
	}
	return now.Sub(last) > timeout
}

func (d *agentDaemon) handleStartConversation(ctx context.Context, start *corev1.StartConversation) {
	conversationID := strings.TrimSpace(start.GetConversationId())
	conv, err := d.createConversation(conversationID)
	if err != nil {
		d.sendSystemError(ctx, conversationID, err)
		return
	}

	slice := start.GetSlice()
	if slice == nil || strings.TrimSpace(slice.GetAccount()) == "" || strings.TrimSpace(slice.GetSlice()) == "" {
		err := userError("invalid_agent_conversation", "server started an agent conversation without a valid slice", "Try creating a new conversation.")
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}
	conv.setConversationInfo(
		strings.TrimSpace(start.GetTitle()),
		strings.TrimSpace(slice.GetAccount()),
		strings.TrimSpace(slice.GetSlice()),
		strings.TrimSpace(start.GetSliceId()),
	)
	// Short-circuit only when the workspace is both marked ready in memory AND
	// still present on disk. A conv loaded as ready (or readied earlier this run)
	// whose dir was deleted out from under us must re-hydrate, or codex would
	// later launch against a missing cmd.Dir and fail with no repair.
	if conv.isReady() && workspaceDirExists(conv.workdir) {
		d.persistConversationMeta(conv)
		_ = d.sendQueue.send(ctx, &corev1.DaemonMessage{
			Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
				ConversationId:  conversationID,
				WorkspaceSubdir: conv.workspaceSubdir,
			}},
		})
		return
	}
	if err := d.hydrateWorkspace(ctx, conv, sliceRefLabel(slice)); err != nil {
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}

	conv.setReady(nil)
	d.persistConversationMeta(conv)
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
	if err := d.baseCtx.Err(); err != nil {
		return
	}
	if !conv.markUserSeq(msg.GetSeq()) {
		return // already ran (or is running) this user message; redelivered duplicate
	}

	// Reuse the conversation's warm runtime session, opening one on the first
	// turn (or after a prior turn tore it down). The process lives on d.baseCtx
	// so it survives per-turn cancellation and server reconnects.
	session, err := conv.ensureSession(d.baseCtx, d.runtime, func(fetchCtx context.Context) (string, error) {
		return d.fetchConversationTranscript(fetchCtx, conversationID)
	})
	if err != nil {
		d.sendSystemError(ctx, conversationID, fmt.Errorf("agent runtime failed: %w", err))
		return
	}
	// Persist the thread id the session resumed/started so a daemon restart can
	// resume the same runtime thread.
	if id := session.ThreadID(); id != "" && id != conv.getThreadID() {
		conv.setThreadID(id)
		d.persistConversationMeta(conv)
	}

	// Root the turn at d.baseCtx, NOT the connection context, so a Connect
	// reconnect (e.g. Cloudflare resetting the stream) does not cancel a running
	// turn. The turn is cancelled only by an explicit server CancelConversation
	// (via conv.setCancel) or daemon shutdown (d.baseCtx). Turn output is buffered
	// per-conversation and resent on reconnect, so events are not lost either.
	runCtx, cancel := context.WithCancel(d.baseCtx)
	conv.setCancel(cancel)
	defer conv.clearCancel(cancel)
	defer cancel()

	err = forwardAgentTurn(runCtx, session, msg.GetText(), conversationID, func(event *corev1.AgentEvent) {
		d.emitAgentTurnEvent(runCtx, conv, event)
	})

	// A cancelled or errored turn can leave the app-server mid-turn or in a bad
	// state; tear the session down so the next turn starts clean and resumes via
	// the persisted thread id.
	if err != nil {
		conv.closeSession()
	}

	if errors.Is(err, context.Canceled) {
		// Suppress the terminal status only on daemon shutdown; a server-initiated
		// cancel should still surface "canceled" to the user.
		if d.baseCtx.Err() == nil {
			d.emitConversationEvent(conv, &corev1.AgentEvent{
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
		if d.baseCtx.Err() == nil {
			d.emitConversationEvent(conv, &corev1.AgentEvent{
				ConversationId: conversationID,
				Role:           "system",
				Type:           "error",
				Text:           fmt.Errorf("agent runtime failed: %w", err).Error(),
				Final:          true,
			})
		}
		return
	}

	// Capture any edits the turn produced as a (conversation-linked) patchset.
	// Use d.baseCtx so a reconnect mid-turn does not skip capture.
	if d.baseCtx.Err() == nil {
		d.capturePatchset(d.baseCtx, conv)
		// Mark the end of the turn so clients can keep the "agent is working"
		// affordance up across interim messages/tool calls and only drop it when
		// the turn truly finishes. A turn can emit several agent messages before
		// it is done, so a finalized message is NOT a reliable end-of-turn signal;
		// this explicit marker is. The cancel/error paths above already emit their
		// own terminal events, so this covers the normal success path. It is a
		// control event (clients hide it from the transcript).
		d.emitConversationEvent(conv, &corev1.AgentEvent{
			ConversationId: conversationID,
			Role:           "system",
			Type:           agentTurnCompleteType,
			Final:          true,
		})
	}
}

func (d *agentDaemon) emitAgentTurnEvent(ctx context.Context, conv *agentConversation, event *corev1.AgentEvent) {
	normalizeAgentWorkspaceEventLinks(event, conv.workdir)
	// Ephemeral token deltas are high-frequency and transient: the finalized
	// event supersedes them (clients coalesce by item_id), so they go
	// best-effort and are not sequenced/acked/resent. Only durable events
	// (finalized messages, tool calls, status, errors) get the ack+resend
	// guarantee — which keeps ack volume to roughly one per finalized item
	// instead of one per token.
	if event.Ephemeral {
		_ = d.sendAgentEvent(ctx, event)
		return
	}
	d.emitConversationEvent(conv, event)
}

// capturePatchset records the workspace edits produced by the latest turn as a
// patchset linked to this conversation. It is best-effort: `gs cs capture` is a
// no-op when there are no edits, and any failure is surfaced as a status event
// rather than failing the turn.
func (d *agentDaemon) capturePatchset(ctx context.Context, conv *agentConversation) {
	title := conv.getTitle()
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
	if out != "" && out != "no changes to capture" && out != "no changes since last patchset" {
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

// handleCloseWorkspace tears a conversation down at the server's request: it
// cancels any in-flight turn, closes the runtime session, drops the conversation
// from the in-memory map, and removes (or archives) its on-disk workspace. The
// conversation's status is already inactive server-side and its transcript is
// persisted, so this only reclaims local state. Best-effort: a conversation not
// resident this run still has its dir removed if present.
func (d *agentDaemon) handleCloseWorkspace(closeMsg *corev1.CloseWorkspace) {
	conversationID := strings.TrimSpace(closeMsg.GetConversationId())
	if conversationID == "" {
		return
	}
	d.mu.Lock()
	conv := d.conversations[conversationID]
	delete(d.conversations, conversationID)
	d.mu.Unlock()

	workdir := ""
	if conv != nil {
		conv.cancelRun()
		conv.closeSession()
		workdir = conv.workdir
	} else if _, dir, err := agentConversationPaths(d.workingDir, conversationID); err == nil {
		workdir = dir
	}
	d.removeOrArchiveWorkspace(workdir, closeMsg.GetDeleteWorkspace())
}

// handleReconcileWorkspaces reaps the local workspace of any conversation the
// daemon holds that the server reports as no longer active. This cleans up
// conversations closed while the daemon was offline, whose live CloseWorkspace
// push was missed. Only settled (ready) conversations are reaped: one that is
// mid-hydration is either an active (re)start — and so present in the active
// set — or in progress, and must be left alone.
func (d *agentDaemon) handleReconcileWorkspaces(reconcile *corev1.ReconcileWorkspaces) {
	active := make(map[string]struct{}, len(reconcile.GetActiveConversationIds()))
	for _, id := range reconcile.GetActiveConversationIds() {
		active[strings.TrimSpace(id)] = struct{}{}
	}
	d.mu.Lock()
	stale := make([]*agentConversation, 0)
	for id, conv := range d.conversations {
		if _, ok := active[id]; ok {
			continue
		}
		if !conv.isReady() {
			continue
		}
		delete(d.conversations, id)
		stale = append(stale, conv)
	}
	d.mu.Unlock()
	for _, conv := range stale {
		conv.cancelRun()
		conv.closeSession()
		d.removeOrArchiveWorkspace(conv.workdir, true)
	}
}

// removeOrArchiveWorkspace deletes the workspace dir when deleteWorkspace is set,
// otherwise moves it under conversations/.archived/. Failures are logged, not
// fatal — the conversation is already closed server-side.
func (d *agentDaemon) removeOrArchiveWorkspace(workdir string, deleteWorkspace bool) {
	if strings.TrimSpace(workdir) == "" {
		return
	}
	if deleteWorkspace {
		if err := os.RemoveAll(workdir); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace remove failed for %s: %v\n", workdir, err)
		}
		return
	}
	archiveDir := filepath.Join(d.workingDir, "conversations", ".archived")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent workspace archive mkdir failed: %v\n", err)
		return
	}
	if err := os.Rename(workdir, filepath.Join(archiveDir, filepath.Base(workdir))); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
	}
}

func workspaceDirExists(workdir string) bool {
	if strings.TrimSpace(workdir) == "" {
		return false
	}
	info, err := os.Stat(workdir)
	return err == nil && info.IsDir()
}

// agentTranscriptSeedLimit caps the rendered transcript handed to a cold runtime
// thread, keeping the most recent context when a conversation is very long.
const agentTranscriptSeedLimit = 24000

// fetchConversationTranscript loads the conversation's persisted events from the
// server and renders them as a compact transcript for seeding a cold runtime
// thread. Returns "" when there is nothing to seed (a brand-new conversation).
// Best-effort by contract: the runtime treats any error as "no seed" and starts
// cold rather than failing the turn.
func (d *agentDaemon) fetchConversationTranscript(ctx context.Context, conversationID string) (string, error) {
	conn, err := dial(ctx, d.cfg.ServerAddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	res, err := corev1.NewAgentServiceClient(conn).GetConversationEvents(
		authContext(ctx, d.cfg),
		&corev1.GetConversationEventsRequest{ConversationId: conversationID},
	)
	if err != nil {
		return "", err
	}
	return renderTranscript(res.GetEvents()), nil
}

// renderTranscript turns persisted conversation events into a developer-prompt
// block that gives a freshly started runtime thread the prior context. It keeps
// user/agent messages and tool calls, drops streamed deltas and control events,
// and trims to the most recent agentTranscriptSeedLimit bytes.
func renderTranscript(events []*corev1.ConversationEvent) string {
	lines := make([]string, 0, len(events))
	for _, ev := range events {
		text := strings.TrimSpace(ev.GetText())
		if text == "" {
			continue
		}
		var label string
		switch ev.GetType() {
		case "message":
			switch ev.GetRole() {
			case "user":
				label = "User"
			case "agent":
				label = "Assistant"
			default:
				label = "System"
			}
		case "tool_call":
			label = "Tool call"
		default:
			// Skip streamed deltas, reasoning, tool output, status, error, and
			// control events (turn_complete): they are noise or redundant here.
			continue
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", label, text))
	}
	if len(lines) == 0 {
		return ""
	}
	body := strings.Join(lines, "\n\n")
	if len(body) > agentTranscriptSeedLimit {
		// Keep the most recent context; fix up any rune split by the cut.
		body = strings.ToValidUTF8(body[len(body)-agentTranscriptSeedLimit:], "")
	}
	return "You are resuming an existing conversation. The transcript so far is " +
		"below; use it as context and do not repeat work already completed.\n\n" +
		"--- transcript start ---\n" + body + "\n--- transcript end ---"
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

func (d *agentDaemon) loadExistingConversations() {
	conversationsDir := filepath.Join(d.workingDir, "conversations")
	entries, err := os.ReadDir(conversationsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := readConversationMeta(filepath.Join(conversationsDir, entry.Name(), agentMetaFilename))
		if err != nil {
			continue
		}
		meta.ConversationID = strings.TrimSpace(meta.ConversationID)
		if meta.ConversationID == "" || meta.ConversationID != entry.Name() {
			continue
		}
		subdir, workdir, err := agentConversationPaths(d.workingDir, meta.ConversationID)
		if err != nil {
			continue
		}
		conv := &agentConversation{
			id:              meta.ConversationID,
			workdir:         workdir,
			workspaceSubdir: subdir,
			ready:           make(chan struct{}),
		}
		conv.setConversationInfo(meta.Title, meta.SliceAccount, meta.SliceName, meta.SliceID)
		conv.setThreadID(meta.ThreadID)
		conv.setReady(nil)

		d.mu.Lock()
		if d.conversations[conv.id] == nil {
			d.conversations[conv.id] = conv
		}
		d.mu.Unlock()
	}
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
	event := &corev1.AgentEvent{
		ConversationId: conversationID,
		Role:           "system",
		Type:           "error",
		Text:           err.Error(),
		Final:          true,
	}
	if conv := d.getConversation(conversationID); conv != nil {
		d.emitConversationEvent(conv, event)
		return
	}
	_ = d.sendAgentEvent(ctx, event)
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

func (d *agentDaemon) persistConversationMeta(conv *agentConversation) {
	if err := writeConversationMeta(conv); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent metadata persist failed for conversation %s: %v\n", conv.id, err)
	}
}

func writeConversationMeta(conv *agentConversation) error {
	meta := conv.meta()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(conv.workdir, ".agent-meta-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(conv.workdir, agentMetaFilename)); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func readConversationMeta(path string) (conversationMeta, error) {
	var meta conversationMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
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

func (c *agentConversation) isReady() bool {
	select {
	case <-c.ready:
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		return c.readyErr == nil
	default:
		return false
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

func (c *agentConversation) setConversationInfo(title, sliceAccount, sliceName, sliceID string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.title = title
	c.sliceAccount = sliceAccount
	c.sliceName = sliceName
	c.sliceID = sliceID
}

func (c *agentConversation) getTitle() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.title
}

func (c *agentConversation) meta() conversationMeta {
	threadID := c.getThreadID()
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return conversationMeta{
		ConversationID: c.id,
		Title:          c.title,
		ThreadID:       threadID,
		SliceAccount:   c.sliceAccount,
		SliceName:      c.sliceName,
		SliceID:        c.sliceID,
	}
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

func (c *agentConversation) getThreadID() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.codexThreadID
}

func (c *agentConversation) setThreadID(id string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.codexThreadID = id
}

// markUserSeq records that the user message with this persisted seq is being
// run and reports whether the caller should proceed. A repeat seq is a
// redelivered duplicate (the server resends unanswered user messages after
// ConversationStarted) and returns false. Dedup is a seen-set rather than a
// high-water mark so that two in-flight messages whose goroutines acquire
// runMu out of seq order both still run — a lower seq must never be silently
// dropped. seq 0 (legacy server) always runs.
func (c *agentConversation) markUserSeq(seq int64) bool {
	if seq <= 0 {
		return true
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if _, ok := c.processedUserSeqs[seq]; ok {
		return false
	}
	if c.processedUserSeqs == nil {
		c.processedUserSeqs = map[int64]struct{}{}
	}
	c.processedUserSeqs[seq] = struct{}{}
	return true
}

func (c *agentConversation) ensureSession(ctx context.Context, runtime agentRuntime, freshThreadHistory func(context.Context) (string, error)) (agentSession, error) {
	c.stateMu.Lock()
	if c.session != nil && c.session.Alive() {
		session := c.session
		c.stateMu.Unlock()
		return session, nil
	}
	// A session that died between turns (e.g. the app-server crashed) is stale;
	// drop and tear it down so this turn transparently opens a fresh one and
	// resumes via the persisted thread id.
	stale := c.session
	c.session = nil
	resume := c.codexThreadID
	c.stateMu.Unlock()
	if stale != nil {
		stale.Close()
	}

	// freshThreadHistory seeds a *cold* thread (no resume id, or resume failed)
	// with the persisted transcript so the agent keeps its conversational context
	// even after losing its workspace/.agent-meta (fresh-dir restart, new machine).
	// The runtime invokes it only on the thread/start path, so a successful resume
	// pays no fetch cost.
	session, err := runtime.OpenSession(ctx, c.workdir, resume, agentWorkspaceInstructions(conversationIncludedPaths(c.workdir)), freshThreadHistory)
	if err != nil {
		return nil, err
	}
	c.stateMu.Lock()
	c.session = session
	c.stateMu.Unlock()
	return session, nil
}

func (c *agentConversation) closeSession() {
	c.stateMu.Lock()
	session := c.session
	c.session = nil
	c.stateMu.Unlock()
	if session != nil {
		session.Close()
	}
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

func forwardAgentTurn(ctx context.Context, session agentSession, prompt, conversationID string, emitEvent func(*corev1.AgentEvent)) error {
	return session.RunTurn(ctx, prompt, func(event agentRuntimeEvent) {
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
			ItemId:         event.ItemID,
			Ephemeral:      event.Ephemeral,
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
			Name:              name,
			Runtime:           runtimeName,
			Version:           version,
			ContainerRuntimes: detectContainerRuntimes(),
			AllowHostExec:     true,
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
