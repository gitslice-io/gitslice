package agentd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *Daemon) HandleStartConversation(ctx context.Context, start *corev1.StartConversation) {
	conversationID := strings.TrimSpace(start.GetConversationId())
	if d.Runtime == nil {
		d.sendSystemError(ctx, conversationID, fmt.Errorf("this daemon runs checks only (gs agent start --ci); start a conversation on another daemon"))
		return
	}
	conv, err := d.createConversation(conversationID)
	if err != nil {
		d.sendSystemError(ctx, conversationID, err)
		return
	}

	slice := start.GetSlice()
	if slice == nil || strings.TrimSpace(slice.GetAccount()) == "" || strings.TrimSpace(slice.GetSlice()) == "" {
		err := NewError("invalid_agent_conversation", "server started an agent conversation without a valid slice", "Try creating a new conversation.")
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}
	if !d.AllowsSlice(slice) {
		err := NewError("slice_not_allowed", fmt.Sprintf("daemon is not allowed to run conversations for %s", sliceRefLabel(slice)), "Restart gs agent start without --slice or add --slice for this slice.")
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}
	conv.setConversationInfo(
		strings.TrimSpace(start.GetTitle()),
		strings.TrimSpace(slice.GetAccount()),
		strings.TrimSpace(slice.GetSlice()),
		strings.TrimSpace(start.GetSliceId()),
		strings.TrimSpace(start.GetRole()),
	)
	childEnv, err := d.ensureConversationChildKey(ctx, conv)
	if err != nil {
		err = fmt.Errorf("agent credential delegation failed: %w", err)
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}
	if conv.isReady() && d.conversationWorkspaceUsable(conv.id, conv.workdir) {
		d.persistConversationMeta(conv)
		if d.SendQueue != nil {
			_ = d.SendQueue.Send(ctx, &corev1.DaemonMessage{
				Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
					ConversationId:  conversationID,
					WorkspaceSubdir: conv.workspaceSubdir,
				}},
			})
		}
		return
	}
	if err := d.hydrateWorkspaceWithEnv(ctx, conv, sliceRefLabel(slice), childEnv); err != nil {
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}

	conv.setReady(nil)
	d.persistConversationMeta(conv)
	if d.SendQueue != nil {
		_ = d.SendQueue.Send(ctx, &corev1.DaemonMessage{
			Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
				ConversationId:  conversationID,
				WorkspaceSubdir: conv.workspaceSubdir,
			}},
		})
	}
}

func (d *Daemon) HandleUserMessage(ctx context.Context, msg *corev1.DeliverUserMessage) {
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
	baseCtx := d.checkBaseContext()
	if err := baseCtx.Err(); err != nil {
		return
	}
	if !conv.markUserSeq(msg.GetSeq()) {
		return // already ran (or is running) this user message; redelivered duplicate
	}

	childEnv, err := d.ensureConversationChildKey(baseCtx, conv)
	if err != nil {
		d.sendSystemError(ctx, conversationID, fmt.Errorf("agent credential delegation failed: %w", err))
		return
	}

	session, err := conv.ensureSession(WithRuntimeEnv(baseCtx, childEnv), d.Runtime, func(fetchCtx context.Context) (string, error) {
		return d.fetchConversationTranscript(fetchCtx, conversationID)
	})
	if err != nil {
		d.sendSystemError(ctx, conversationID, fmt.Errorf("agent runtime failed: %w", err))
		return
	}
	if id := session.ThreadID(); id != "" && id != conv.getThreadID() {
		conv.setThreadID(id)
		d.persistConversationMeta(conv)
	}

	runCtx, cancel := context.WithCancel(baseCtx)
	conv.setCancel(cancel)
	defer conv.clearCancel(cancel)
	defer cancel()

	err = ForwardTurn(runCtx, session, msg.GetText(), conversationID, func(event *corev1.AgentEvent) {
		d.EmitAgentTurnEvent(runCtx, conv, event)
	})

	if err != nil {
		conv.closeSession()
	}

	if errors.Is(err, context.Canceled) {
		if baseCtx.Err() == nil {
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
		if baseCtx.Err() == nil {
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

	if baseCtx.Err() == nil {
		d.capturePatchset(baseCtx, conv)
		d.emitConversationEvent(conv, &corev1.AgentEvent{
			ConversationId: conversationID,
			Role:           "system",
			Type:           TurnCompleteType,
			Final:          true,
		})
	}
}

func (d *Daemon) EmitAgentTurnEvent(ctx context.Context, conv *Conversation, event *corev1.AgentEvent) {
	NormalizeWorkspaceEventLinks(event, conv.workdir)
	if event.Ephemeral {
		_ = d.sendAgentEvent(ctx, event)
		return
	}
	d.emitConversationEvent(conv, event)
}

func (d *Daemon) capturePatchset(ctx context.Context, conv *Conversation) {
	if d.Host.CaptureChangeset == nil {
		return
	}
	title := conv.getTitle()
	if title == "" {
		title = "agent: " + conv.id
	}
	childEnv, err := d.ensureConversationChildKey(ctx, conv)
	if err != nil {
		_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
			ConversationId: conv.id,
			Role:           "system",
			Type:           "error",
			Text:           "patchset capture failed: " + err.Error(),
		})
		return
	}
	captured, summary, err := d.Host.CaptureChangeset(ctx, conv.workdir, childEnv, title)
	if err != nil {
		_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
			ConversationId: conv.id,
			Role:           "system",
			Type:           "error",
			Text:           "patchset capture failed: " + err.Error(),
		})
		return
	}
	if captured {
		_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
			ConversationId: conv.id,
			Role:           "system",
			Type:           "status",
			Text:           summary,
		})
	}
}

func (d *Daemon) HandleCancelConversation(cancel *corev1.CancelConversation) {
	conv := d.getConversation(strings.TrimSpace(cancel.GetConversationId()))
	if conv == nil {
		return
	}
	conv.cancelRun()
}

func (d *Daemon) fetchConversationTranscript(ctx context.Context, conversationID string) (string, error) {
	conn, err := d.Host.dial(ctx, d.Host.ServerAddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	res, err := corev1.NewAgentServiceClient(conn).GetConversationEvents(
		d.Host.authContext(ctx),
		&corev1.GetConversationEventsRequest{ConversationId: conversationID},
	)
	if err != nil {
		return "", err
	}
	return RenderTranscript(res.GetEvents()), nil
}

func sliceRefLabel(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return ref.Account + ":" + ref.Slice
}
