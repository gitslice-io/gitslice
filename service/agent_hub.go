package service

import (
	"sync"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// agentHub is the in-memory relay between connected agent daemons and live web
// StreamConversation subscribers. It holds no durable state: every conversation
// event is persisted via the AgentStore, and the hub only routes live traffic.
// See design/16_bring_your_own_agent.md.
type agentHub struct {
	mu sync.Mutex
	// daemons holds each daemon's connections, one per process
	// (RegisterDaemon.instance_id), the newest last.
	daemons     map[string][]*daemonConn
	subscribers map[string]map[int64]chan *corev1.ConversationEvent
	nextSub     int64
}

func newAgentHub() *agentHub {
	return &agentHub{
		daemons:     map[string][]*daemonConn{},
		subscribers: map[string]map[int64]chan *corev1.ConversationEvent{},
	}
}

// daemonConn is the server side of a daemon's Connect stream. The Connect
// handler owns a writer goroutine that drains send and forwards to the stream.
type daemonConn struct {
	daemonID string
	// instance names the daemon process in check run claims
	// (storage.InstanceKey); capacity is how many checks it runs at once, 0
	// for no limit (design/32 layer 3).
	instance string
	capacity int
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

// registerDaemon adds a connection for a process of the daemon, replacing an
// earlier connection of the same process (a reconnect).
func (h *agentHub) registerDaemon(daemonID, instance string, capacity int) *daemonConn {
	conn := &daemonConn{
		daemonID: daemonID,
		instance: instance,
		capacity: capacity,
		send:     make(chan *corev1.ServerMessage, 64),
		done:     make(chan struct{}),
	}
	h.mu.Lock()
	kept := h.daemons[daemonID][:0]
	for _, prev := range h.daemons[daemonID] {
		if prev.instance == instance {
			close(prev.done)
			continue
		}
		kept = append(kept, prev)
	}
	h.daemons[daemonID] = append(kept, conn)
	h.mu.Unlock()
	return conn
}

func (h *agentHub) unregisterDaemon(conn *daemonConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := h.daemons[conn.daemonID]
	for i, c := range conns {
		if c == conn {
			close(conn.done)
			conns = append(conns[:i:i], conns[i+1:]...)
			break
		}
	}
	if len(conns) == 0 {
		delete(h.daemons, conn.daemonID)
	} else {
		h.daemons[conn.daemonID] = conns
	}
}

// daemon returns the daemon's newest connection.
func (h *agentHub) daemon(daemonID string) (*daemonConn, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := h.daemons[daemonID]
	if len(conns) == 0 {
		return nil, false
	}
	return conns[len(conns)-1], true
}

// conns returns every connection of the daemon, one per process.
func (h *agentHub) conns(daemonID string) []*daemonConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*daemonConn(nil), h.daemons[daemonID]...)
}

func (h *agentHub) onlineDaemonIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.daemons))
	for id := range h.daemons {
		ids = append(ids, id)
	}
	return ids
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
