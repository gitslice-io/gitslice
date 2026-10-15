package cli

import (
	"context"
	"strings"
	"sync"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type checkRunOutbox struct {
	outMu          sync.Mutex
	pending        []*corev1.CheckRunUpdate
	ackedClientSeq int64
	flushedSeq     int64
	flushQueue     *agentSendQueue
	terminal       bool
}

func (d *agentDaemon) emitCheckRunError(runID string, seq int64, err error) {
	if err == nil {
		return
	}
	d.emitCheckRunLog(runID, &seq, err.Error())
	d.emitCheckRunTerminal(runID, &seq, "errored", -1, oneLineSummary(err.Error()))
}

func (d *agentDaemon) emitCheckRunRunning(runID string, seq *int64) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	*seq++
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{
		RunId:     runID,
		Status:    "running",
		ClientSeq: *seq,
	})
}

func (d *agentDaemon) emitCheckRunLog(runID string, seq *int64, log string) {
	if strings.TrimSpace(runID) == "" || log == "" {
		return
	}
	*seq++
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{
		RunId:     runID,
		LogChunk:  log,
		Stream:    "stdout",
		ClientSeq: *seq,
	})
}

func (d *agentDaemon) emitCheckRunTerminal(runID string, seq *int64, status string, exitCode int32, summary string) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	*seq++
	d.sendCheckRunUpdate(&corev1.CheckRunUpdate{
		RunId:     runID,
		Status:    status,
		ExitCode:  exitCode,
		ClientSeq: *seq,
		Final:     true,
		Summary:   summary,
	})
}

func (d *agentDaemon) sendCheckRunUpdate(update *corev1.CheckRunUpdate) {
	if update == nil {
		return
	}
	runID := strings.TrimSpace(update.GetRunId())
	if runID == "" {
		return
	}
	update.RunId = runID

	outbox := d.lockCurrentCheckRunOutboxForSend(runID)
	outbox.pending = append(outbox.pending, update)
	if update.GetFinal() {
		outbox.terminal = true
	}
	outbox.dropOverflowLocked()
	d.flushCheckRunPendingLocked(outbox)
	outbox.outMu.Unlock()

	d.enforceCheckRunOutboxLimit()
}

func (d *agentDaemon) lockCurrentCheckRunOutboxForSend(runID string) *checkRunOutbox {
	for {
		outbox := d.ensureCheckRunOutbox(runID)
		outbox.outMu.Lock()
		current := d.isCurrentCheckRunOutbox(runID, outbox)
		if current {
			return outbox
		}
		outbox.outMu.Unlock()
	}
}

func (d *agentDaemon) ensureCheckRunOutbox(runID string) *checkRunOutbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkRunOutboxes == nil {
		d.checkRunOutboxes = map[string]*checkRunOutbox{}
	}
	if outbox := d.checkRunOutboxes[runID]; outbox != nil {
		return outbox
	}
	outbox := &checkRunOutbox{}
	d.checkRunOutboxes[runID] = outbox
	d.checkRunOutboxOrder = append(d.checkRunOutboxOrder, runID)
	return outbox
}

func (d *agentDaemon) isCurrentCheckRunOutbox(runID string, outbox *checkRunOutbox) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkRunOutboxes[runID] == outbox
}

func (outbox *checkRunOutbox) dropOverflowLocked() {
	for len(outbox.pending) > agentMaxPendingCheckRunUpdates {
		drop := -1
		for i, update := range outbox.pending {
			if isLogOnlyCheckRunUpdate(update) {
				drop = i
				break
			}
		}
		if drop < 0 {
			return
		}
		copy(outbox.pending[drop:], outbox.pending[drop+1:])
		outbox.pending[len(outbox.pending)-1] = nil
		outbox.pending = outbox.pending[:len(outbox.pending)-1]
	}
}

func isLogOnlyCheckRunUpdate(update *corev1.CheckRunUpdate) bool {
	return update != nil && update.GetStatus() == "" && !update.GetFinal()
}

func (d *agentDaemon) flushCheckRunPendingLocked(outbox *checkRunOutbox) {
	if outbox == nil {
		return
	}
	d.mu.Lock()
	sq := d.sendQueue
	d.mu.Unlock()
	if sq == nil {
		return
	}
	if sq != outbox.flushQueue {
		outbox.flushQueue = sq
		outbox.flushedSeq = outbox.ackedClientSeq
	}
	for _, update := range outbox.pending {
		if update.GetClientSeq() <= outbox.flushedSeq {
			continue
		}
		msg := &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_CheckUpdate{CheckUpdate: update}}
		if err := sq.send(context.Background(), msg); err != nil {
			return
		}
		outbox.flushedSeq = update.GetClientSeq()
	}
}

func (d *agentDaemon) enforceCheckRunOutboxLimit() {
	for {
		d.mu.Lock()
		if len(d.checkRunOutboxes) <= agentMaxCheckRunOutboxes {
			d.mu.Unlock()
			return
		}
		order := append([]string(nil), d.checkRunOutboxOrder...)
		d.mu.Unlock()

		victim := d.oldestEvictableCheckRunOutbox(order)
		if victim == "" {
			return
		}
		d.mu.Lock()
		if d.checkRunOutboxes[victim] != nil {
			delete(d.checkRunOutboxes, victim)
			d.removeCheckRunOutboxOrderLocked(victim)
		}
		d.mu.Unlock()
	}
}

func (d *agentDaemon) oldestEvictableCheckRunOutbox(order []string) string {
	oldestTerminal := ""
	for _, runID := range order {
		d.mu.Lock()
		outbox := d.checkRunOutboxes[runID]
		d.mu.Unlock()
		if outbox == nil {
			continue
		}
		outbox.outMu.Lock()
		terminal := outbox.terminal
		stale := terminal && len(outbox.pending) == 0
		outbox.outMu.Unlock()
		if stale {
			return runID
		}
		if terminal && oldestTerminal == "" {
			oldestTerminal = runID
		}
	}
	return oldestTerminal
}

func (d *agentDaemon) removeCheckRunOutboxOrderLocked(runID string) {
	for i, existing := range d.checkRunOutboxOrder {
		if existing == runID {
			copy(d.checkRunOutboxOrder[i:], d.checkRunOutboxOrder[i+1:])
			d.checkRunOutboxOrder[len(d.checkRunOutboxOrder)-1] = ""
			d.checkRunOutboxOrder = d.checkRunOutboxOrder[:len(d.checkRunOutboxOrder)-1]
			return
		}
	}
}

func (d *agentDaemon) handleCheckRunAck(ack *corev1.CheckRunAck) {
	if ack == nil {
		return
	}
	runID := strings.TrimSpace(ack.GetRunId())
	if runID == "" {
		return
	}
	d.mu.Lock()
	outbox := d.checkRunOutboxes[runID]
	d.mu.Unlock()
	if outbox == nil {
		return
	}

	outbox.outMu.Lock()
	if ack.GetAckedClientSeq() > outbox.ackedClientSeq {
		outbox.ackedClientSeq = ack.GetAckedClientSeq()
	}
	kept := outbox.pending[:0]
	for _, update := range outbox.pending {
		if update.GetClientSeq() > outbox.ackedClientSeq {
			kept = append(kept, update)
		}
	}
	for i := len(kept); i < len(outbox.pending); i++ {
		outbox.pending[i] = nil
	}
	outbox.pending = kept
	if len(outbox.pending) == 0 && outbox.terminal {
		d.mu.Lock()
		if d.checkRunOutboxes[runID] == outbox {
			delete(d.checkRunOutboxes, runID)
			d.removeCheckRunOutboxOrderLocked(runID)
		}
		d.mu.Unlock()
	}
	outbox.outMu.Unlock()
}
