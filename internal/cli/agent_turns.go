package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *agentDaemon) handleStartConversation(ctx context.Context, start *corev1.StartConversation) {
	conversationID := strings.TrimSpace(start.GetConversationId())
	if d.runtime == nil {
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
		err := userError("invalid_agent_conversation", "server started an agent conversation without a valid slice", "Try creating a new conversation.")
		conv.setReady(err)
		d.sendSystemError(ctx, conversationID, err)
		return
	}
	if !d.allowsSlice(slice) {
		err := userError("slice_not_allowed", fmt.Sprintf("daemon is not allowed to run conversations for %s", sliceRefLabel(slice)), "Restart gs agent start without --slice or add --slice for this slice.")
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
	// Short-circuit only when the workspace is both marked ready in memory AND
	// still present on disk. A conv loaded as ready (or readied earlier this run)
	// whose dir was deleted out from under us must re-hydrate, or codex would
	// later launch against a missing cmd.Dir and fail with no repair.
	if conv.isReady() && d.conversationWorkspaceUsable(conv.id, conv.workdir) {
		d.persistConversationMeta(conv)
		_ = d.sendQueue.send(ctx, &corev1.DaemonMessage{
			Payload: &corev1.DaemonMessage_Started{Started: &corev1.ConversationStarted{
				ConversationId:  conversationID,
				WorkspaceSubdir: conv.workspaceSubdir,
			}},
		})
		return
	}
	if err := d.hydrateWorkspaceWithEnv(ctx, conv, sliceRefLabel(slice), childEnv); err != nil {
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

	childEnv, err := d.ensureConversationChildKey(d.baseCtx, conv)
	if err != nil {
		d.sendSystemError(ctx, conversationID, fmt.Errorf("agent credential delegation failed: %w", err))
		return
	}

	// Reuse the conversation's warm runtime session, opening one on the first
	// turn (or after a prior turn tore it down). The process lives on d.baseCtx
	// so it survives per-turn cancellation and server reconnects.
	session, err := conv.ensureSession(withAgentRuntimeEnv(d.baseCtx, childEnv), d.runtime, func(fetchCtx context.Context) (string, error) {
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
	// The capture runs in this process, in a Runner scoped to the
	// conversation's workspace and identity; what it did comes back as data.
	child := d.childRunner(conv.workdir, childEnv, io.Discard)
	res, err := child.captureChangeset(ctx, commandOptions{Format: "text", Verbose: true}, title, false)
	if err != nil {
		_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
			ConversationId: conv.id,
			Role:           "system",
			Type:           "error",
			Text:           "patchset capture failed: " + err.Error(),
		})
		return
	}
	if res.Outcome == captureCaptured {
		_ = d.sendAgentEvent(ctx, &corev1.AgentEvent{
			ConversationId: conv.id,
			Role:           "system",
			Type:           "status",
			Text:           res.Summary(),
		})
	}
}

func (d *agentDaemon) handleCancelConversation(cancel *corev1.CancelConversation) {
	conv := d.getConversation(strings.TrimSpace(cancel.GetConversationId()))
	if conv == nil {
		return
	}
	conv.cancelRun()
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
