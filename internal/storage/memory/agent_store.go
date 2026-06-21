package memory

import (
	"context"
	"sort"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

// AgentStore is the in-memory implementation of storage.AgentStore used by tests
// and the memory-backed server. See design/16_bring_your_own_agent.md.
type AgentStore struct{ b *backend }

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *AgentStore) RegisterDaemon(ctx context.Context, in storage.AgentDaemonInput) (*corev1.AgentDaemon, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := nowRFC()
	// Reuse an existing daemon for the same account+name so a restart keeps its id.
	for _, d := range s.b.agentDaemons {
		if d.Name == in.Name && d.Account == in.Account {
			d.Runtime = in.Runtime
			d.Version = in.Version
			d.Status = "online"
			d.LastSeenAt = now
			return cloneDaemon(d), nil
		}
	}
	id, err := objectid.RandomID("agent")
	if err != nil {
		return nil, err
	}
	d := &corev1.AgentDaemon{
		Id:         id,
		Account:    in.Account,
		Name:       in.Name,
		Runtime:    in.Runtime,
		Version:    in.Version,
		Status:     "online",
		LastSeenAt: now,
		CreatedAt:  now,
	}
	s.b.agentDaemons[id] = d
	return cloneDaemon(d), nil
}

func (s *AgentStore) SetDaemonStatus(ctx context.Context, daemonID, status string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	d, ok := s.b.agentDaemons[daemonID]
	if !ok {
		return storage.ErrNotFound
	}
	d.Status = status
	d.LastSeenAt = nowRFC()
	return nil
}

func (s *AgentStore) GetDaemon(ctx context.Context, daemonID string) (*corev1.AgentDaemon, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	d, ok := s.b.agentDaemons[daemonID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return cloneDaemon(d), nil
}

func (s *AgentStore) ListDaemons(ctx context.Context, subjectID string) ([]*corev1.AgentDaemon, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.AgentDaemon
	for _, d := range s.b.agentDaemons {
		out = append(out, cloneDaemon(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (s *AgentStore) CreateConversation(ctx context.Context, in storage.ConversationInput) (*corev1.Conversation, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.agentDaemons[in.DaemonID]; !ok {
		return nil, storage.ErrNotFound
	}
	id, err := objectid.RandomID("conv")
	if err != nil {
		return nil, err
	}
	now := nowRFC()
	c := &corev1.Conversation{
		Id:              id,
		DaemonId:        in.DaemonID,
		SliceId:         in.SliceID,
		Slice:           &corev1.SliceRef{Account: in.Account, Slice: in.SliceName},
		Title:           in.Title,
		Status:          "active",
		WorkspaceSubdir: "conversations/" + id,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.b.agentConvs[id] = c
	s.b.agentConvNextSeq[id] = 1
	return cloneConversation(c), nil
}

func (s *AgentStore) GetConversation(ctx context.Context, conversationID string) (*corev1.Conversation, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.agentConvs[conversationID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return cloneConversation(c), nil
}

func (s *AgentStore) ListConversations(ctx context.Context, filter storage.ConversationFilter) ([]*corev1.Conversation, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.Conversation
	for _, c := range s.b.agentConvs {
		if filter.SliceID != "" && c.SliceId != filter.SliceID {
			continue
		}
		if filter.DaemonID != "" && c.DaemonId != filter.DaemonID {
			continue
		}
		out = append(out, cloneConversation(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (s *AgentStore) SetConversationStatus(ctx context.Context, conversationID, status string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.agentConvs[conversationID]
	if !ok {
		return storage.ErrNotFound
	}
	c.Status = status
	c.UpdatedAt = nowRFC()
	return nil
}

func (s *AgentStore) AppendEvent(ctx context.Context, conversationID, role, eventType, text, dataJSON, itemID string) (*corev1.ConversationEvent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.agentConvs[conversationID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	seq := s.b.agentConvNextSeq[conversationID]
	if seq == 0 {
		seq = 1
	}
	s.b.agentConvNextSeq[conversationID] = seq + 1
	id, err := objectid.RandomID("ev")
	if err != nil {
		return nil, err
	}
	ev := &corev1.ConversationEvent{
		Id:             id,
		ConversationId: conversationID,
		Seq:            seq,
		Role:           role,
		Type:           eventType,
		Text:           text,
		DataJson:       dataJSON,
		CreatedAt:      nowRFC(),
		ItemId:         itemID,
	}
	s.b.agentEvents[conversationID] = append(s.b.agentEvents[conversationID], ev)
	c.UpdatedAt = ev.CreatedAt
	return cloneEvent(ev), nil
}

func (s *AgentStore) ListEvents(ctx context.Context, conversationID string, afterSeq int64) ([]*corev1.ConversationEvent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.ConversationEvent
	for _, ev := range s.b.agentEvents[conversationID] {
		if ev.Seq > afterSeq {
			out = append(out, cloneEvent(ev))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (s *AgentStore) ListEventsRange(ctx context.Context, conversationID string, afterSeq, beforeSeq int64) ([]*corev1.ConversationEvent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.ConversationEvent
	for _, ev := range s.b.agentEvents[conversationID] {
		if ev.Seq <= afterSeq {
			continue
		}
		if beforeSeq > 0 && ev.Seq > beforeSeq {
			continue
		}
		out = append(out, cloneEvent(ev))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (s *AgentStore) LatestEventSeq(ctx context.Context, conversationID string) (int64, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var max int64
	for _, ev := range s.b.agentEvents[conversationID] {
		if ev.Seq > max {
			max = ev.Seq
		}
	}
	return max, nil
}

func cloneDaemon(d *corev1.AgentDaemon) *corev1.AgentDaemon {
	c := *d
	return &c
}

func cloneConversation(c *corev1.Conversation) *corev1.Conversation {
	out := *c
	if c.Slice != nil {
		sr := *c.Slice
		out.Slice = &sr
	}
	return &out
}

func cloneEvent(e *corev1.ConversationEvent) *corev1.ConversationEvent {
	c := *e
	return &c
}
