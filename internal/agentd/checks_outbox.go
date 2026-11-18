package agentd

import (
	"context"
	"sync"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	MaxPendingCheckRunUpdates = 256
	MaxCheckRunOutboxes       = 128
)

type CheckRunOutbox struct {
	OutMu          sync.Mutex
	Pending        []*corev1.CheckRunUpdate
	AckedClientSeq int64
	FlushedSeq     int64
	FlushQueue     *SendQueue
	Terminal       bool
}

func (outbox *CheckRunOutbox) AppendLocked(update *corev1.CheckRunUpdate) {
	outbox.Pending = append(outbox.Pending, update)
	if update.GetFinal() {
		outbox.Terminal = true
	}
	outbox.DropOverflowLocked()
}

func (outbox *CheckRunOutbox) DropOverflowLocked() {
	for len(outbox.Pending) > MaxPendingCheckRunUpdates {
		drop := -1
		for i, update := range outbox.Pending {
			if IsLogOnlyCheckRunUpdate(update) {
				drop = i
				break
			}
		}
		if drop < 0 {
			return
		}
		copy(outbox.Pending[drop:], outbox.Pending[drop+1:])
		outbox.Pending[len(outbox.Pending)-1] = nil
		outbox.Pending = outbox.Pending[:len(outbox.Pending)-1]
	}
}

func IsLogOnlyCheckRunUpdate(update *corev1.CheckRunUpdate) bool {
	return update != nil && update.GetStatus() == "" && !update.GetFinal()
}

func (outbox *CheckRunOutbox) FlushLocked(sq *SendQueue) {
	if outbox == nil || sq == nil {
		return
	}
	if sq != outbox.FlushQueue {
		outbox.FlushQueue = sq
		outbox.FlushedSeq = outbox.AckedClientSeq
	}
	for _, update := range outbox.Pending {
		if update.GetClientSeq() <= outbox.FlushedSeq {
			continue
		}
		msg := &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_CheckUpdate{CheckUpdate: update}}
		if err := sq.Send(context.Background(), msg); err != nil {
			return
		}
		outbox.FlushedSeq = update.GetClientSeq()
	}
}

// AckLocked prunes updates <= ackedSeq and reports whether this outbox is now
// terminal and completely drained (ready to evict).
func (outbox *CheckRunOutbox) AckLocked(ackedSeq int64) bool {
	if ackedSeq > outbox.AckedClientSeq {
		outbox.AckedClientSeq = ackedSeq
	}
	kept := outbox.Pending[:0]
	for _, update := range outbox.Pending {
		if update.GetClientSeq() > outbox.AckedClientSeq {
			kept = append(kept, update)
		}
	}
	for i := len(kept); i < len(outbox.Pending); i++ {
		outbox.Pending[i] = nil
	}
	outbox.Pending = kept
	return len(outbox.Pending) == 0 && outbox.Terminal
}
