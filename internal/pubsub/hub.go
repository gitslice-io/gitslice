// Package pubsub provides a lightweight, thread-safe, non-blocking topic
// fan-out hub for live event streaming. It holds no durable state; slow
// subscribers drop live items and recover from persistent storage replay.
package pubsub

import "sync"

// Hub broadcasts items of type T to active subscribers keyed by topic.
type Hub[T any] struct {
	buffer      int
	mu          sync.Mutex
	subscribers map[string]map[int64]chan T
	nextSub     int64
}

// NewHub creates a Hub whose subscriber channels have the given buffer size.
func NewHub[T any](buffer int) *Hub[T] {
	if buffer <= 0 {
		buffer = 1
	}
	return &Hub[T]{
		buffer:      buffer,
		subscribers: map[string]map[int64]chan T{},
	}
}

// Subscribe registers a buffered channel for topic and returns an idempotent
// cancel function that removes the subscription and prunes empty topic maps.
func (h *Hub[T]) Subscribe(topic string) (<-chan T, func()) {
	ch := make(chan T, h.buffer)
	h.mu.Lock()
	h.nextSub++
	id := h.nextSub
	subs := h.subscribers[topic]
	if subs == nil {
		subs = map[int64]chan T{}
		h.subscribers[topic] = subs
	}
	subs[id] = ch
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			subs := h.subscribers[topic]
			if subs == nil {
				return
			}
			delete(subs, id)
			if len(subs) == 0 {
				delete(h.subscribers, topic)
			}
		})
	}
	return ch, cancel
}

// Publish performs best-effort, non-blocking delivery of item to every active
// subscriber of topic. Safe to call on a nil Hub.
func (h *Hub[T]) Publish(topic string, item T) {
	if h == nil {
		return
	}
	h.mu.Lock()
	subs := h.subscribers[topic]
	chans := make([]chan T, 0, len(subs))
	for _, ch := range subs {
		chans = append(chans, ch)
	}
	h.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- item:
		default:
		}
	}
}
