package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *agentDaemon) handleServerMessage(ctx context.Context, cancel context.CancelFunc, msg *corev1.ServerMessage) {
	switch payload := msg.GetPayload().(type) {
	case *corev1.ServerMessage_Start:
		d.beginWork()
		go func() {
			defer d.endWork()
			d.handleStartConversation(ctx, payload.Start)
		}()
	case *corev1.ServerMessage_UserMessage:
		d.beginWork()
		go func() {
			defer d.endWork()
			d.handleUserMessage(ctx, payload.UserMessage)
		}()
	case *corev1.ServerMessage_Cancel:
		d.markActivity()
		d.handleCancelConversation(payload.Cancel)
	case *corev1.ServerMessage_Close:
		d.markActivity()
		go d.handleCloseWorkspace(payload.Close)
	case *corev1.ServerMessage_Reconcile:
		d.markActivity()
		go d.handleReconcileWorkspaces(payload.Reconcile)
	case *corev1.ServerMessage_Ping:
		if err := d.sendDaemonMessage(ctx, heartbeatDaemonMessage()); err != nil {
			cancel()
		}
	case *corev1.ServerMessage_Ack:
		d.handleEventAck(payload.Ack)
	case *corev1.ServerMessage_RunChecks:
		fmt.Fprintf(d.runner.stderr(), "agent received RunChecks: checks=%d run_ids=%v\n", len(payload.RunChecks.GetChecks()), runCheckIDs(payload.RunChecks))
		d.beginWork()
		go func() {
			defer d.endWork()
			d.handleRunChecks(payload.RunChecks)
		}()
	case *corev1.ServerMessage_CancelCheck:
		d.markActivity()
		d.handleCancelCheckRun(payload.CancelCheck)
	case *corev1.ServerMessage_CheckAck:
		d.markActivity()
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

// resolvedAgentName is the name a daemon registers with: the one given, else
// this machine's.
func resolvedAgentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultAgentName()
	}
	return name
}

func registerDaemonMessage(name, runtimeName string) *corev1.DaemonMessage {
	name = resolvedAgentName(name)
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
