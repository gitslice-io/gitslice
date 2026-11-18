package cli

import (
	"strings"

	"gitslice.io/gitslice/internal/agentd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type checkRunOutbox = agentd.CheckRunOutbox

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
	outbox.AppendLocked(update)
	d.flushCheckRunPendingLocked(outbox)
	outbox.OutMu.Unlock()

	d.enforceCheckRunOutboxLimit()
}

func (d *agentDaemon) lockCurrentCheckRunOutboxForSend(runID string) *checkRunOutbox {
	for {
		outbox := d.ensureCheckRunOutbox(runID)
		outbox.OutMu.Lock()
		current := d.isCurrentCheckRunOutbox(runID, outbox)
		if current {
			return outbox
		}
		outbox.OutMu.Unlock()
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

func (d *agentDaemon) flushCheckRunPendingLocked(outbox *checkRunOutbox) {
	d.mu.Lock()
	sq := d.sendQueue
	d.mu.Unlock()
	outbox.FlushLocked(sq)
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
		outbox.OutMu.Lock()
		terminal := outbox.Terminal
		stale := terminal && len(outbox.Pending) == 0
		outbox.OutMu.Unlock()
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

	outbox.OutMu.Lock()
	if outbox.AckLocked(ack.GetAckedClientSeq()) {
		d.mu.Lock()
		if d.checkRunOutboxes[runID] == outbox {
			delete(d.checkRunOutboxes, runID)
			d.removeCheckRunOutboxOrderLocked(runID)
		}
		d.mu.Unlock()
	}
	outbox.OutMu.Unlock()
}
