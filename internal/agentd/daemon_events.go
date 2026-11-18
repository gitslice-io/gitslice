package agentd

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/checkexec"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *Daemon) HandleServerMessage(ctx context.Context, cancel context.CancelFunc, msg *corev1.ServerMessage) {
	switch payload := msg.GetPayload().(type) {
	case *corev1.ServerMessage_Start:
		d.beginWork()
		go func() {
			defer d.endWork()
			d.HandleStartConversation(ctx, payload.Start)
		}()
	case *corev1.ServerMessage_UserMessage:
		d.beginWork()
		go func() {
			defer d.endWork()
			d.HandleUserMessage(ctx, payload.UserMessage)
		}()
	case *corev1.ServerMessage_Cancel:
		d.markActivity()
		d.HandleCancelConversation(payload.Cancel)
	case *corev1.ServerMessage_Close:
		d.markActivity()
		go d.HandleCloseWorkspace(payload.Close)
	case *corev1.ServerMessage_Reconcile:
		d.markActivity()
		go d.HandleReconcileWorkspaces(payload.Reconcile)
	case *corev1.ServerMessage_Ping:
		if err := d.sendDaemonMessage(ctx, HeartbeatDaemonMessage()); err != nil {
			cancel()
		}
	case *corev1.ServerMessage_Ack:
		d.HandleEventAck(payload.Ack)
	case *corev1.ServerMessage_RunChecks:
		fmt.Fprintf(d.Host.stderr(), "agent received RunChecks: checks=%d run_ids=%v\n", len(payload.RunChecks.GetChecks()), RunCheckIDs(payload.RunChecks))
		d.beginWork()
		go func() {
			defer d.endWork()
			d.HandleRunChecks(payload.RunChecks)
		}()
	case *corev1.ServerMessage_CancelCheck:
		d.markActivity()
		d.HandleCancelCheckRun(payload.CancelCheck)
	case *corev1.ServerMessage_CheckAck:
		d.markActivity()
		d.HandleCheckRunAck(payload.CheckAck)
	}
}

func (d *Daemon) emitConversationEvent(conv *Conversation, event *corev1.AgentEvent) {
	conv.outMu.Lock()
	defer conv.outMu.Unlock()
	conv.nextClientSeq++
	event.ClientSeq = conv.nextClientSeq
	conv.pending = append(conv.pending, event)
	if len(conv.pending) > MaxPendingEvents {
		fmt.Fprintf(d.Host.stderr(), "agent outbound buffer overflow for %s; dropping oldest event\n", conv.id)
		conv.pending = conv.pending[1:]
	}
	d.flushPendingLocked(conv)
}

func (d *Daemon) flushPendingLocked(conv *Conversation) {
	d.mu.Lock()
	sq := d.SendQueue
	d.mu.Unlock()
	if sq == nil {
		return
	}
	if sq != conv.flushQueue {
		conv.flushQueue = sq
		conv.flushedSeq = conv.ackedClientSeq
	}
	for _, ev := range conv.pending {
		if ev.ClientSeq <= conv.flushedSeq {
			continue
		}
		msg := &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Event{Event: ev}}
		if err := sq.Send(context.Background(), msg); err != nil {
			return
		}
		conv.flushedSeq = ev.ClientSeq
	}
}

func (d *Daemon) HandleEventAck(ack *corev1.EventAck) {
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

func (d *Daemon) resendPendingAll() {
	d.mu.Lock()
	convs := make([]*Conversation, 0, len(d.conversations))
	for _, conv := range d.conversations {
		convs = append(convs, conv)
	}
	checkOutboxes := make([]*CheckRunOutbox, 0, len(d.checkRunOutboxes))
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
		outbox.OutMu.Lock()
		d.flushCheckRunPendingLocked(outbox)
		outbox.OutMu.Unlock()
	}
}

func (d *Daemon) sendAgentEvent(ctx context.Context, event *corev1.AgentEvent) error {
	return d.sendDaemonMessage(ctx, &corev1.DaemonMessage{
		Payload: &corev1.DaemonMessage_Event{Event: event},
	})
}

func (d *Daemon) sendDaemonMessage(ctx context.Context, msg *corev1.DaemonMessage) error {
	d.mu.Lock()
	sendQueue := d.SendQueue
	d.mu.Unlock()
	if sendQueue == nil {
		return context.Canceled
	}
	return sendQueue.Send(ctx, msg)
}

func (d *Daemon) sendSystemError(ctx context.Context, conversationID string, err error) {
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

func (d *Daemon) registerDaemonMessage(name, runtimeName, instanceID string, concurrency int) *corev1.DaemonMessage {
	return RegisterDaemonMessage(name, runtimeName, d.Host.Version, instanceID, concurrency, !checkexec.RequireContainerChecks())
}
