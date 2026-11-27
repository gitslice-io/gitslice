package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/doorbell"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// AgentService implements the bring-your-own-agent API: a relay between agent
// daemons (Connect) and the web UI (unary + StreamConversation). See
// design/16_bring_your_own_agent.md.
type AgentService struct {
	Auth        storage.AuthStore
	Slices      storage.SliceStore
	Agents      storage.AgentStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Checks      storage.CheckStore
	ObjectStore ObjectStore

	hub        *agentHub
	checkLogs  *checkLogHub
	dispatcher *checkDispatcher
	serverAddr string
	// doorbell wakes daemons waiting on standby (design/32 layer 1).
	doorbell *doorbell.Client
	gate     *activity.Gate
}

func (s *AgentService) activityGate() *activity.Gate {
	if s != nil && s.gate != nil {
		return s.gate
	}
	return activity.Default
}

func normalizeConversationRole(role string) (string, authz.Action, error) {
	normalized, err := storage.NormalizeConversationRole(role)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, "role must be reader, contributor, or writer")
	}
	switch normalized {
	case "reader":
		return normalized, authz.ActionRead, nil
	case "writer":
		return normalized, authz.ActionSubmit, nil
	default:
		return normalized, authz.ActionPropose, nil
	}
}

func (s *AgentService) authorizeConversationControl(ctx context.Context, subjectID string, conv *corev1.Conversation) error {
	_, action, err := normalizeConversationRole(conv.GetRole())
	if err != nil {
		return err
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, conv.Slice, action); err != nil {
		return err
	}
	if _, err := s.ownedDaemon(ctx, subjectID, conv.DaemonId); err == nil {
		return nil
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, conv.Slice, authz.ActionWrite); err != nil {
		return err
	}
	return nil
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
	convRole, action, err := normalizeConversationRole(req.GetRole())
	if err != nil {
		return nil, err
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, action)
	if err != nil {
		return nil, err
	}
	daemon, err := s.ownedDaemon(ctx, subjectID, req.DaemonId)
	if err != nil {
		return nil, err
	}
	if daemon.GetRuntime() == ciDaemonRuntime {
		name := daemon.GetDisplayName()
		if name == "" {
			name = daemon.GetName()
		}
		return nil, status.Errorf(codes.FailedPrecondition, "daemon %s runs checks only (gs agent start --ci); start the conversation on another daemon", name)
	}
	conv, err := s.Agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  req.DaemonId,
		SubjectID: subjectID,
		SliceID:   slice.Id,
		Account:   req.Slice.Account,
		SliceName: req.Slice.Slice,
		Title:     req.Title,
		Role:      convRole,
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
			Role:           conv.Role,
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
	if p, hasPrincipal := authctx.PrincipalFrom(ctx); hasPrincipal && p.KeyID != "" && p.SubjectID == subjectID && p.HasSliceScope {
		filtered := make([]*corev1.Conversation, 0, len(convs))
		for _, conv := range convs {
			if p.AllowsSlice(conv.GetSliceId()) {
				filtered = append(filtered, conv)
			}
		}
		convs = filtered
	}
	for _, conv := range convs {
		s.annotateConversation(conv)
	}
	return &corev1.ListConversationsResponse{Conversations: convs}, nil
}

func (s *AgentService) GetConversation(ctx context.Context, req *corev1.GetConversationRequest) (*corev1.Conversation, error) {
	// Reads follow the slice's visibility: public slices expose their
	// conversations to anyone, so authorize against the slice (ActionRead)
	// rather than requiring a subject up front. SendAgentMessage stays
	// write-gated so only authorized members can talk.
	subjectID := optionalSubject(ctx)
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
	if err := s.authorizeConversationControl(ctx, subjectID, conv); err != nil {
		return nil, err
	}
	ev, _, err := s.Agents.AppendEvent(ctx, conv.Id, "user", "message", req.Text, "", "", 0)
	if err != nil {
		return nil, grpcError(err)
	}
	s.hub.publish(conv.Id, ev)
	if conn, ok := s.hub.daemon(conv.DaemonId); ok {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_UserMessage{UserMessage: &corev1.DeliverUserMessage{
			ConversationId: conv.Id,
			Text:           req.Text,
			Seq:            ev.Seq,
		}}})
	}
	return &corev1.SendAgentMessageResponse{Event: ev}, nil
}

func (s *AgentService) StreamConversation(req *corev1.StreamConversationRequest, stream corev1.AgentService_StreamConversationServer) error {
	ctx := stream.Context()
	subjectID := optionalSubject(ctx)
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
	events = s.rewriteConversationLinks(ctx, conv, events)
	lastSeq := req.AfterSeq
	for _, ev := range events {
		if err := stream.Send(ev); err != nil {
			return grpcError(err)
		}
		lastSeq = ev.GetSeq()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if ev.GetSeq() <= lastSeq {
				continue
			}
			rewritten := s.rewriteConversationLinks(ctx, conv, []*corev1.ConversationEvent{ev})
			if len(rewritten) > 0 {
				ev = rewritten[0]
			}
			if err := stream.Send(ev); err != nil {
				return grpcError(err)
			}
			lastSeq = ev.GetSeq()
		}
	}
}

func (s *AgentService) GetConversationEvents(ctx context.Context, req *corev1.GetConversationEventsRequest) (*corev1.GetConversationEventsResponse, error) {
	subjectID := optionalSubject(ctx)
	conv, err := s.Agents.GetConversation(ctx, req.ConversationId)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, conv.Slice, authz.ActionRead); err != nil {
		return nil, err
	}
	events, err := s.Agents.ListEventsRange(ctx, req.ConversationId, req.AfterSeq, req.BeforeSeq, req.Limit)
	if err != nil {
		return nil, grpcError(err)
	}
	events = s.rewriteConversationLinks(ctx, conv, events)
	s.annotateConversation(conv)
	return &corev1.GetConversationEventsResponse{Conversation: conv, Events: events}, nil
}

// CloseConversation marks a conversation inactive and, when its daemon is
// online, tells the daemon to tear down and delete the on-disk workspace. The
// status write is durable and happens regardless of daemon reachability; the
// persisted event transcript is kept. Closing is write-gated like SendAgentMessage.
func (s *AgentService) CloseConversation(ctx context.Context, req *corev1.CloseConversationRequest) (*corev1.Conversation, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.ConversationId == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation_id is required")
	}
	conv, err := s.Agents.GetConversation(ctx, req.ConversationId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.authorizeConversationControl(ctx, subjectID, conv); err != nil {
		return nil, err
	}
	if err := s.Agents.SetConversationStatus(ctx, conv.Id, "inactive"); err != nil {
		return nil, grpcError(err)
	}
	// Best-effort live teardown. If the daemon is offline the dir lingers until a
	// future reap; the durable inactive status above is what matters for replay
	// (replayDaemonConversations only re-starts active conversations).
	if conn, ok := s.hub.daemon(conv.DaemonId); ok {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_Close{Close: &corev1.CloseWorkspace{
			ConversationId:  conv.Id,
			DeleteWorkspace: true,
		}}})
	}
	updated, err := s.Agents.GetConversation(ctx, conv.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	s.annotateConversation(updated)
	return updated, nil
}

// rewriteConversationLinks rewrites gsfile: links in event text to web URLs.
// It is best-effort: patchset lookup failures degrade to slice-file links. The
// returned slice never mutates input events; events with rewritten text are
// cloned first because live hub events are shared across subscribers.
func (s *AgentService) rewriteConversationLinks(ctx context.Context, conv *corev1.Conversation, events []*corev1.ConversationEvent) []*corev1.ConversationEvent {
	if conv == nil {
		return events
	}
	slice := conv.GetSlice()
	account := slice.GetAccount()
	slug := slice.GetSlice()
	if account == "" || slug == "" {
		return events
	}
	rc := linkRewriteContext{
		account:        account,
		slug:           slug,
		conversationID: conv.GetId(),
	}
	// Only touch the database when at least one event actually carries a link;
	// most streamed events (token deltas, status) have none, and this method is
	// called per live event, so an unconditional lookup would query on every one.
	hasLink := false
	for _, ev := range events {
		if ev != nil && textMayContainAgentFileLink(ev.GetText(), rc) {
			hasLink = true
			break
		}
	}
	if !hasLink {
		return events
	}
	var patchsets []*corev1.Patchset
	if s.Changesets != nil {
		if loaded, err := s.Changesets.PatchsetsByConversation(ctx, conv.GetId()); err == nil {
			patchsets = loaded
		}
	}
	var includedPaths []string
	if s.Slices != nil {
		if loaded, err := s.Slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: slug}); err == nil && loaded.GetDefinition() != nil {
			includedPaths = loaded.GetDefinition().GetIncludedPaths()
		}
	}
	out := make([]*corev1.ConversationEvent, len(events))
	for i, ev := range events {
		if ev == nil || !textMayContainAgentFileLink(ev.GetText(), rc) {
			out[i] = ev
			continue
		}
		cloned := proto.Clone(ev).(*corev1.ConversationEvent)
		eventContext := rc
		eventContext.seq = ev.GetSeq()
		eventContext.includedPaths = includedPaths
		eventContext.patchsets = patchsets
		cloned.Text = rewriteAgentFileLinks(cloned.GetText(), eventContext)
		out[i] = cloned
	}
	return out
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

// ciDaemonRuntime is the runtime a daemon started with gs agent start --ci
// registers: it runs checks and takes no conversations.
const ciDaemonRuntime = "ci"

func (s *AgentService) ownedDaemon(ctx context.Context, subjectID, daemonID string) (*corev1.AgentDaemon, error) {
	daemons, err := s.Agents.ListDaemons(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	for _, d := range daemons {
		if d.Id == daemonID {
			return d, nil
		}
	}
	return nil, status.Error(codes.NotFound, "daemon not found")
}

// daemonAccountForSubject attributes a connecting daemon to the caller's
// personal account when they have one, or to the single account they
// administer when they do not. Non-admin organization members cannot claim an
// organization's daemon namespace.
func (s *AgentService) daemonAccountForSubject(ctx context.Context, subjectID string) (string, error) {
	if s.Auth == nil {
		return "", nil
	}
	if personal := newSubjectDirectory(s.Auth).bestEffortUsername(ctx, subjectID); personal != "" {
		return personal, nil
	}
	accounts, err := s.Auth.ListSubjectAccountSlugs(ctx, subjectID)
	if err != nil {
		return "", grpcError(err)
	}
	var adminAccounts []string
	for _, raw := range accounts {
		acct := strings.TrimSpace(raw)
		if acct == "" {
			continue
		}
		ok, err := canAuthorizeAccount(ctx, s.Auth, subjectID, acct, authz.ActionAdmin)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		if kind, err := s.Auth.AccountKind(ctx, acct); err == nil && kind == "personal" {
			return acct, nil
		}
		adminAccounts = append(adminAccounts, acct)
	}
	if len(adminAccounts) == 1 {
		return adminAccounts[0], nil
	}
	return "", status.Error(codes.PermissionDenied, "daemon registration requires a personal account or an unambiguous administered account")
}
