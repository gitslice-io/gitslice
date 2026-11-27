// Package agenthub implements the in-memory relay between connected agent
// daemons and live conversation stream subscribers. It holds no durable state:
// every conversation event is persisted in storage, and the hub only routes
// live traffic (design/16_bring_your_own_agent.md).
package agenthub

import (
	"sync"

	"gitslice.io/gitslice/internal/pubsub"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Hub tracks live daemon connections (multiple processes per daemon) and fans
// out live conversation events via pubsub.Hub.
type Hub struct {
	mu      sync.Mutex
	daemons map[string][]*DaemonConn
	events  *pubsub.Hub[*corev1.ConversationEvent]
}

// New constructs an empty agent Hub.
func New() *Hub {
	return &Hub{
		daemons: map[string][]*DaemonConn{},
		events:  pubsub.NewHub[*corev1.ConversationEvent](256),
	}
}

// DaemonConn is the server side of one daemon process's Connect stream.
type DaemonConn struct {
	DaemonID string
	// Instance names the daemon process in check run claims
	// (storage.InstanceKey); Capacity is how many checks it runs at once
	// (0 for unlimited, design/32 layer 3).
	Instance string
	Capacity int
	Send     chan *corev1.ServerMessage
	Done     chan struct{}

	// runs holds the check runs sent to this process that have not ended.
	// While it holds any, heartbeats keep the runs' claims alive even when
	// the server is otherwise idle.
	runsMu sync.Mutex
	runs   map[string]struct{}
}

func (c *DaemonConn) TrackRun(runID string) {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	if c.runs == nil {
		c.runs = map[string]struct{}{}
	}
	c.runs[runID] = struct{}{}
}

func (c *DaemonConn) UntrackRun(runID string) {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	delete(c.runs, runID)
}

func (c *DaemonConn) HasRuns() bool {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	return len(c.runs) > 0
}

func (c *DaemonConn) TrySend(msg *corev1.ServerMessage) bool {
	select {
	case <-c.Done:
		return false
	default:
	}
	select {
	case <-c.Done:
		return false
	case c.Send <- msg:
		return true
	}
}

// RegisterDaemon adds a connection for a process of the daemon, replacing an
// earlier connection of the same process (a reconnect).
func (h *Hub) RegisterDaemon(daemonID, instance string, capacity int) *DaemonConn {
	conn := &DaemonConn{
		DaemonID: daemonID,
		Instance: instance,
		Capacity: capacity,
		Send:     make(chan *corev1.ServerMessage, 64),
		Done:     make(chan struct{}),
	}
	h.mu.Lock()
	kept := h.daemons[daemonID][:0]
	for _, prev := range h.daemons[daemonID] {
		if prev.Instance == instance {
			close(prev.Done)
			continue
		}
		kept = append(kept, prev)
	}
	h.daemons[daemonID] = append(kept, conn)
	h.mu.Unlock()
	return conn
}

func (h *Hub) UnregisterDaemon(conn *DaemonConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := h.daemons[conn.DaemonID]
	for i, c := range conns {
		if c == conn {
			close(conn.Done)
			conns = append(conns[:i:i], conns[i+1:]...)
			break
		}
	}
	if len(conns) == 0 {
		delete(h.daemons, conn.DaemonID)
	} else {
		h.daemons[conn.DaemonID] = conns
	}
}

// Daemon returns the daemon's newest connection.
func (h *Hub) Daemon(daemonID string) (*DaemonConn, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := h.daemons[daemonID]
	if len(conns) == 0 {
		return nil, false
	}
	return conns[len(conns)-1], true
}

// Conns returns every connection of the daemon, one per process.
func (h *Hub) Conns(daemonID string) []*DaemonConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*DaemonConn(nil), h.daemons[daemonID]...)
}

func (h *Hub) OnlineDaemonIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.daemons))
	for id := range h.daemons {
		ids = append(ids, id)
	}
	return ids
}

// Subscribe registers a live listener for a conversation's events.
func (h *Hub) Subscribe(conversationID string) (<-chan *corev1.ConversationEvent, func()) {
	return h.events.Subscribe(conversationID)
}

// Publish fans an event out to all live subscribers of a conversation.
func (h *Hub) Publish(conversationID string, ev *corev1.ConversationEvent) {
	h.events.Publish(conversationID, ev)
}
