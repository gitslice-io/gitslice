package service

import (
	"sync"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

// agentHub is the in-memory relay between connected agent daemons and live web
// StreamConversation subscribers. It holds no durable state: every conversation
// event is persisted via the AgentStore, and the hub only routes live traffic.
// See design/16_bring_your_own_agent.md.
type agentHub struct {
	mu          sync.Mutex
	daemons     map[string]*daemonConn
	subscribers map[string]map[int64]chan *corev1.ConversationEvent
	nextSub     int64
}

func newAgentHub() *agentHub {
	return &agentHub{
		daemons:     map[string]*daemonConn{},
		subscribers: map[string]map[int64]chan *corev1.ConversationEvent{},
	}
}

// daemonConn is the server side of a daemon's Connect stream. The Connect
// handler owns a writer goroutine that drains send and forwards to the stream.
type daemonConn struct {
	daemonID string
	send     chan *corev1.ServerMessage
	done     chan struct{}
}

func (c *daemonConn) trySend(msg *corev1.ServerMessage) bool {
	select {
	case c.send <- msg:
		return true
	case <-c.done:
		return false
	}
}

func (h *agentHub) registerDaemon(daemonID string) *daemonConn {
	conn := &daemonConn{
		daemonID: daemonID,
		send:     make(chan *corev1.ServerMessage, 64),
		done:     make(chan struct{}),
	}
	h.mu.Lock()
	if prev := h.daemons[daemonID]; prev != nil {
		// Drop a stale connection for the same daemon id (e.g. reconnect).
		close(prev.done)
	}
	h.daemons[daemonID] = conn
	h.mu.Unlock()
	return conn
}

func (h *agentHub) unregisterDaemon(conn *daemonConn) {
	h.mu.Lock()
	if h.daemons[conn.daemonID] == conn {
		delete(h.daemons, conn.daemonID)
		close(conn.done)
	}
	h.mu.Unlock()
}

func (h *agentHub) daemon(daemonID string) (*daemonConn, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conn, ok := h.daemons[daemonID]
	return conn, ok
}

func (h *agentHub) subscribe(conversationID string) (int64, chan *corev1.ConversationEvent) {
	ch := make(chan *corev1.ConversationEvent, 256)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSub++
	id := h.nextSub
	subs := h.subscribers[conversationID]
	if subs == nil {
		subs = map[int64]chan *corev1.ConversationEvent{}
		h.subscribers[conversationID] = subs
	}
	subs[id] = ch
	return id, ch
}

func (h *agentHub) unsubscribe(conversationID string, id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.subscribers[conversationID]
	if subs == nil {
		return
	}
	delete(subs, id)
	if len(subs) == 0 {
		delete(h.subscribers, conversationID)
	}
}

// publish fans an event out to all live subscribers of a conversation. Delivery
// is best-effort and non-blocking: a slow subscriber that overflows its buffer
// drops the live event and recovers by re-streaming from the persisted log.
func (h *agentHub) publish(conversationID string, ev *corev1.ConversationEvent) {
	h.mu.Lock()
	subs := h.subscribers[conversationID]
	chans := make([]chan *corev1.ConversationEvent, 0, len(subs))
	for _, ch := range subs {
		chans = append(chans, ch)
	}
	h.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- ev:
		default:
		}
	}
}
