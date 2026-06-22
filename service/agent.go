package service

import (
	"context"
	"errors"
	"io"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AgentService implements the bring-your-own-agent API: a relay between agent
// daemons (Connect) and the web UI (unary + StreamConversation). See
// design/16_bring_your_own_agent.md.
type AgentService struct {
	Auth   storage.AuthStore
	Slices storage.SliceStore
	Agents storage.AgentStore

	hub        *agentHub
	serverAddr string
}

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

	account := ""
	if accounts, err := s.Auth.ListSubjectAccountSlugs(ctx, subjectID); err == nil && len(accounts) > 0 {
		account = accounts[0]
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

	conn := s.hub.registerDaemon(daemon.Id)
	defer func() {
		s.hub.unregisterDaemon(conn)
		_ = s.Agents.SetDaemonStatus(context.WithoutCancel(ctx), daemon.Id, "offline")
	}()

	if err := stream.Send(&corev1.ServerMessage{
		Payload: &corev1.ServerMessage_Registered{Registered: &corev1.DaemonRegistered{DaemonId: daemon.Id}},
	}); err != nil {
		return grpcError(err)
	}

	// Writer goroutine: gRPC allows one concurrent Send alongside the Recv loop.
	go func() {
		for {
			select {
			case msg := <-conn.send:
				if err := stream.Send(msg); err != nil {
					return
				}
			case <-conn.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	go s.replayDaemonConversations(ctx, daemon.Id, conn)

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
		case *corev1.DaemonMessage_Started:
			// Workspace ready; nothing to persist for now.
		case *corev1.DaemonMessage_Event:
			ev := p.Event
			if ev.ConversationId == "" {
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
			stored, err := s.Agents.AppendEvent(ctx, ev.ConversationId, role, eventType, ev.Text, ev.DataJson, ev.ItemId)
			if err != nil {
				continue
			}
			s.hub.publish(ev.ConversationId, stored)
		}
	}
}

func (s *AgentService) replayDaemonConversations(ctx context.Context, daemonID string, conn *daemonConn) {
	convs, err := s.Agents.ListConversations(ctx, storage.ConversationFilter{DaemonID: daemonID})
	if err != nil {
		return
	}
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
		}}}) {
			return
		}
	}
}

func (s *AgentService) ListDaemons(ctx context.Context, _ *corev1.ListDaemonsRequest) (*corev1.ListDaemonsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	daemons, err := s.Agents.ListDaemons(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListDaemonsResponse{Daemons: daemons}, nil
}

func (s *AgentService) CreateConversation(ctx context.Context, req *corev1.CreateConversationRequest) (*corev1.Conversation, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.DaemonId == "" {
		return nil, status.Error(codes.InvalidArgument, "daemon_id is required")
	}
	if req.Slice == nil {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnedDaemon(ctx, subjectID, req.DaemonId); err != nil {
		return nil, err
	}
	conv, err := s.Agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  req.DaemonId,
		SubjectID: subjectID,
		SliceID:   slice.Id,
		Account:   req.Slice.Account,
		SliceName: req.Slice.Slice,
		Title:     req.Title,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	if conn, ok := s.hub.daemon(req.DaemonId); ok {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_Start{Start: &corev1.StartConversation{
			ConversationId: conv.Id,
			Slice:          conv.Slice,
			SliceId:        conv.SliceId,
			ServerAddr:     s.serverAddr,
			Title:          conv.Title,
		}}})
	}
	s.annotateConversation(conv)
	return conv, nil
}

func (s *AgentService) ListConversations(ctx context.Context, req *corev1.ListConversationsRequest) (*corev1.ListConversationsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	filter := storage.ConversationFilter{DaemonID: req.DaemonId}
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		filter.SliceID = slice.Id
	} else {
		filter.SubjectID = subjectID
	}
	convs, err := s.Agents.ListConversations(ctx, filter)
	if err != nil {
		return nil, grpcError(err)
	}
	for _, conv := range convs {
		s.annotateConversation(conv)
	}
	return &corev1.ListConversationsResponse{Conversations: convs}, nil
}

func (s *AgentService) GetConversation(ctx context.Context, req *corev1.GetConversationRequest) (*corev1.Conversation, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := s.Agents.GetConversation(ctx, req.ConversationId)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, conv.Slice, authz.ActionRead); err != nil {
		return nil, err
	}
	s.annotateConversation(conv)
	return conv, nil
}

func (s *AgentService) SendAgentMessage(ctx context.Context, req *corev1.SendAgentMessageRequest) (*corev1.SendAgentMessageResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Text == "" {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}
	conv, err := s.Agents.GetConversation(ctx, req.ConversationId)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, conv.Slice, authz.ActionWrite); err != nil {
		return nil, err
	}
	ev, err := s.Agents.AppendEvent(ctx, conv.Id, "user", "message", req.Text, "", "")
	if err != nil {
		return nil, grpcError(err)
	}
	s.hub.publish(conv.Id, ev)
	if conn, ok := s.hub.daemon(conv.DaemonId); ok {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_UserMessage{UserMessage: &corev1.DeliverUserMessage{
			ConversationId: conv.Id,
			Text:           req.Text,
		}}})
	}
	return &corev1.SendAgentMessageResponse{Event: ev}, nil
}

func (s *AgentService) StreamConversation(req *corev1.StreamConversationRequest, stream corev1.AgentService_StreamConversationServer) error {
	ctx := stream.Context()
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return err
	}
	conv, err := s.Agents.GetConversation(ctx, req.ConversationId)
	if err != nil {
		return grpcError(err)
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, conv.Slice, authz.ActionRead); err != nil {
		return err
	}

	// Subscribe before replaying so no event slips through the gap; the seq
	// cursor below de-duplicates any event that appears in both replay and live.
	subID, ch := s.hub.subscribe(req.ConversationId)
	defer s.hub.unsubscribe(req.ConversationId, subID)

	events, err := s.Agents.ListEvents(ctx, req.ConversationId, req.AfterSeq)
	if err != nil {
		return grpcError(err)
	}
	lastSeq := req.AfterSeq
	for _, ev := range events {
		if err := stream.Send(ev); err != nil {
			return grpcError(err)
		}
		lastSeq = ev.Seq
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-ch:
			if ev.Seq <= lastSeq {
				continue
			}
			if err := stream.Send(ev); err != nil {
				return grpcError(err)
			}
			lastSeq = ev.Seq
		}
	}
}

func (s *AgentService) GetConversationEvents(ctx context.Context, req *corev1.GetConversationEventsRequest) (*corev1.GetConversationEventsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := s.Agents.GetConversation(ctx, req.ConversationId)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, conv.Slice, authz.ActionRead); err != nil {
		return nil, err
	}
	events, err := s.Agents.ListEventsRange(ctx, req.ConversationId, req.AfterSeq, req.BeforeSeq)
	if err != nil {
		return nil, grpcError(err)
	}
	s.annotateConversation(conv)
	return &corev1.GetConversationEventsResponse{Conversation: conv, Events: events}, nil
}

func (s *AgentService) annotateConversation(c *corev1.Conversation) {
	if c == nil {
		return
	}
	c.DaemonOnline = false
	if c.GetDaemonId() == "" {
		return
	}
	_, ok := s.hub.daemon(c.GetDaemonId())
	c.DaemonOnline = ok
}

func (s *AgentService) requireOwnedDaemon(ctx context.Context, subjectID, daemonID string) error {
	daemons, err := s.Agents.ListDaemons(ctx, subjectID)
	if err != nil {
		return grpcError(err)
	}
	for _, d := range daemons {
		if d.Id == daemonID {
			return nil
		}
	}
	return status.Error(codes.NotFound, "daemon not found")
}
