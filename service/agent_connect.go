package service

import (
	"context"
	"errors"
	"io"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// agentDaemonPingInterval is how often the server pushes a keepalive Ping on
// each daemon Connect stream. The daemon's long-lived Connect stream produces
// no server->daemon frames between turns, so a proxy in front of the server
// (nginx grpc_read_timeout, the Cloudflare edge) resets the streamed response
// once its idle window elapses — observed at ~125s in staging — tearing the
// stream down. Pinging well inside that window keeps it warm; the daemon answers
// each Ping with a heartbeat, keeping the daemon->server direction alive too.
// Must stay comfortably below the shortest read/idle timeout of any proxy.
const agentDaemonPingInterval = 20 * time.Second

// Connect is the daemon's long-lived bidirectional channel. The first message
// must be RegisterDaemon; the server replies DaemonRegistered and then relays
// StartConversation / DeliverUserMessage commands while persisting and fanning
// out the daemon's AgentEvents.
func (s *AgentService) Connect(stream corev1.AgentService_ConnectServer) error {
	ctx := stream.Context()
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return status.Error(codes.InvalidArgument, "register message is required")
	}
	if err != nil {
		return grpcError(err)
	}
	reg := first.GetRegister()
	if reg == nil {
		return status.Error(codes.InvalidArgument, "first message must be register")
	}

	account, err := s.daemonAccountForSubject(ctx, subjectID)
	if err != nil {
		return err
	}
	daemon, err := s.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: subjectID,
		Account:   account,
		Name:      reg.Name,
		Runtime:   reg.Runtime,
		Version:   reg.Version,
	})
	if err != nil {
		return grpcError(err)
	}

	conn := s.hub.registerDaemon(daemon.Id, storage.InstanceKey(daemon.Id, reg.GetInstanceId()), int(max(reg.GetCheckConcurrency(), 0)))
	// A daemon that detaches on purpose stays online (it polls with
	// StandbyPoll); any other end of the stream marks it offline, unless
	// another of its processes is still connected.
	detached := false
	defer func() {
		s.hub.unregisterDaemon(conn)
		background := context.WithoutCancel(ctx)
		// Queued runs this process was sent but never started go back to
		// the daemon's other processes (design/32 layer 3). Running ones keep
		// their claim until it lapses, so a reconnect carries on with them.
		if s.Checks != nil {
			_ = s.Checks.ReleaseCheckRunClaims(background, conn.instance)
		}
		if !detached && len(s.hub.conns(daemon.Id)) == 0 {
			_ = s.Agents.SetDaemonStatus(background, daemon.Id, "offline")
		}
		if s.dispatcher != nil {
			s.dispatcher.dispatchQueued(background, daemon.Id)
		}
	}()

	if err := stream.Send(&corev1.ServerMessage{
		Payload: &corev1.ServerMessage_Registered{Registered: &corev1.DaemonRegistered{DaemonId: daemon.Id}},
	}); err != nil {
		return grpcError(err)
	}

	// Writer goroutine: gRPC allows one concurrent Send alongside the Recv loop,
	// so every server->daemon write (queued messages and keepalive pings) is
	// funneled through here. The ping ticker keeps the stream from idling out at
	// a proxy in front of the server; see agentDaemonPingInterval.
	go func() {
		ping := time.NewTicker(agentDaemonPingInterval)
		defer ping.Stop()
		for {
			select {
			case msg := <-conn.send:
				if err := stream.Send(msg); err != nil {
					return
				}
			case <-ping.C:
				if err := stream.Send(&corev1.ServerMessage{
					Payload: &corev1.ServerMessage_Ping{Ping: &corev1.Ping{}},
				}); err != nil {
					return
				}
			case <-conn.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		s.replayDaemonConversations(ctx, daemon.Id, conn)
		s.replayDaemonCheckRuns(ctx, daemon.Id, conn)
	}()

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return grpcError(err)
		}
		switch p := msg.Payload.(type) {
		case *corev1.DaemonMessage_Heartbeat:
			_ = s.Agents.SetDaemonStatus(ctx, daemon.Id, "online")
			if s.Checks != nil {
				_ = s.Checks.RenewCheckRunClaims(ctx, conn.instance, storage.CheckRunLease)
			}
		case *corev1.DaemonMessage_Detach:
			if reg.Runtime != ciDaemonRuntime {
				return status.Error(codes.FailedPrecondition, "only a checks-only daemon (gs agent start --ci) can detach")
			}
			// The daemon has nothing running and polls from here on. Count
			// this as a sign of life, so it stays online until its first poll.
			_ = s.Agents.SetDaemonStatus(ctx, daemon.Id, "online")
			detached = true
			return nil
		case *corev1.DaemonMessage_Started:
			s.redeliverUnansweredUserMessages(ctx, daemon.Id, conn, p.Started.GetConversationId())
		case *corev1.DaemonMessage_Event:
			ev := p.Event
			if ev.ConversationId == "" {
				continue
			}
			conv, err := s.Agents.GetConversation(ctx, ev.ConversationId)
			if err != nil || conv.GetDaemonId() != daemon.Id {
				continue
			}
			role := ev.Role
			if role == "" {
				role = "agent"
			}
			eventType := ev.Type
			if eventType == "" {
				eventType = "message"
			}
			stored, inserted, err := s.Agents.AppendEvent(ctx, ev.ConversationId, role, eventType, ev.Text, ev.DataJson, ev.ItemId, ev.ClientSeq)
			if err != nil {
				continue
			}
			// Only fan out a genuinely new event; a resent duplicate (deduped by
			// client_seq) was already published when first stored.
			if inserted {
				s.hub.publish(ev.ConversationId, stored)
			}
			// Ack sequenced events so the daemon can drop them from its resend
			// buffer. Ack duplicates too: a dup means it is already durable.
			if ev.ClientSeq > 0 {
				conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_Ack{Ack: &corev1.EventAck{
					ConversationId: ev.ConversationId,
					AckedClientSeq: ev.ClientSeq,
				}}})
			}
		case *corev1.DaemonMessage_CheckUpdate:
			s.handleCheckRunUpdate(ctx, daemon.Id, conn, p.CheckUpdate)
		}
	}
}

// redeliverUnansweredUserMessages pushes the conversation's trailing
// unanswered user messages to the daemon. Called when the daemon reports a
// conversation's workspace ready (ConversationStarted): any user message sent
// while the daemon was offline/half-open was lost (SendAgentMessage's push is
// best-effort), and the register-time StartConversation replay guarantees a
// Started follows for every active conversation, so this is the redelivery
// point. The daemon dedups on seq, so redelivering a message that actually
// arrived (e.g. a turn still running with no durable output yet) is safe.
func (s *AgentService) redeliverUnansweredUserMessages(ctx context.Context, daemonID string, conn *daemonConn, conversationID string) {
	conv, err := s.Agents.GetConversation(ctx, conversationID)
	if err != nil || conv.DaemonId != daemonID {
		return
	}
	events, err := s.Agents.UnansweredUserEvents(ctx, conversationID)
	if err != nil {
		return
	}
	for _, ev := range events {
		if !conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_UserMessage{UserMessage: &corev1.DeliverUserMessage{
			ConversationId: conversationID,
			Text:           ev.Text,
			Seq:            ev.Seq,
		}}}) {
			return
		}
	}
}

func (s *AgentService) replayDaemonConversations(ctx context.Context, daemonID string, conn *daemonConn) {
	convs, err := s.Agents.ListConversations(ctx, storage.ConversationFilter{DaemonID: daemonID})
	if err != nil {
		return
	}
	activeIDs := make([]string, 0, len(convs))
	for _, conv := range convs {
		if conv == nil || (conv.Status != "" && conv.Status != "active") {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_Start{Start: &corev1.StartConversation{
			ConversationId: conv.Id,
			Slice:          conv.Slice,
			SliceId:        conv.SliceId,
			ServerAddr:     s.serverAddr,
			Title:          conv.Title,
			Role:           conv.Role,
		}}}) {
			return
		}
		activeIDs = append(activeIDs, conv.Id)
	}
	// After replaying the active set, tell the daemon exactly which conversations
	// are still active so it can reap the on-disk workspaces of any it holds that
	// were closed while it was offline (the live CloseWorkspace push was missed).
	conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_Reconcile{Reconcile: &corev1.ReconcileWorkspaces{
		ActiveConversationIds: activeIDs,
	}}})
}
