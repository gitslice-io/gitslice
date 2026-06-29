package service

import (
	"sync"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

// checkLogHub fans out live check logs and status wakeups. It holds no durable
// state; CheckStore remains the replay source of truth.
type checkLogHub struct {
	mu          sync.Mutex
	subscribers map[string]map[int64]chan *corev1.CheckRunLog
	nextSub     int64
}

func newCheckLogHub() *checkLogHub {
	return &checkLogHub{
		subscribers: map[string]map[int64]chan *corev1.CheckRunLog{},
	}
}

func (h *checkLogHub) subscribe(runID string) (<-chan *corev1.CheckRunLog, func()) {
	ch := make(chan *corev1.CheckRunLog, 256)
	h.mu.Lock()
	h.nextSub++
	id := h.nextSub
	subs := h.subscribers[runID]
	if subs == nil {
		subs = map[int64]chan *corev1.CheckRunLog{}
		h.subscribers[runID] = subs
	}
	subs[id] = ch
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			subs := h.subscribers[runID]
			if subs == nil {
				return
			}
			delete(subs, id)
			if len(subs) == 0 {
				delete(h.subscribers, runID)
			}
		})
	}
	return ch, cancel
}

// publish is best-effort and non-blocking. A nil log is a status wakeup used by
// StreamCheckRun to re-check terminal state after flushing persisted logs.
func (h *checkLogHub) publish(runID string, log *corev1.CheckRunLog) {
	if h == nil {
		return
	}
	h.mu.Lock()
	subs := h.subscribers[runID]
	chans := make([]chan *corev1.CheckRunLog, 0, len(subs))
	for _, ch := range subs {
		chans = append(chans, ch)
	}
	h.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- log:
		default:
		}
	}
}
