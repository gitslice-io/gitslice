package agentd

import (
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *Daemon) emitCheckRunError(runID string, seq int64, err error) {
	if err == nil {
		return
	}
	d.emitCheckRunLog(runID, &seq, err.Error())
	d.emitCheckRunTerminal(runID, &seq, "errored", -1, OneLineSummary(err.Error()))
}

func (d *Daemon) emitCheckRunRunning(runID string, seq *int64) {
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

func (d *Daemon) emitCheckRunLog(runID string, seq *int64, log string) {
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

func (d *Daemon) emitCheckRunTerminal(runID string, seq *int64, status string, exitCode int32, summary string) {
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

func (d *Daemon) sendCheckRunUpdate(update *corev1.CheckRunUpdate) {
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

func (d *Daemon) lockCurrentCheckRunOutboxForSend(runID string) *CheckRunOutbox {
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

func (d *Daemon) ensureCheckRunOutbox(runID string) *CheckRunOutbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkRunOutboxes == nil {
		d.checkRunOutboxes = map[string]*CheckRunOutbox{}
	}
	if outbox := d.checkRunOutboxes[runID]; outbox != nil {
		return outbox
	}
	outbox := &CheckRunOutbox{}
	d.checkRunOutboxes[runID] = outbox
	d.checkRunOutboxOrder = append(d.checkRunOutboxOrder, runID)
	return outbox
}

func (d *Daemon) isCurrentCheckRunOutbox(runID string, outbox *CheckRunOutbox) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkRunOutboxes[runID] == outbox
}

func (d *Daemon) flushCheckRunPendingLocked(outbox *CheckRunOutbox) {
	d.mu.Lock()
	sq := d.SendQueue
	d.mu.Unlock()
	outbox.FlushLocked(sq)
}

func (d *Daemon) enforceCheckRunOutboxLimit() {
	for {
		d.mu.Lock()
		if len(d.checkRunOutboxes) <= MaxCheckRunOutboxes {
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

func (d *Daemon) oldestEvictableCheckRunOutbox(order []string) string {
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

func (d *Daemon) removeCheckRunOutboxOrderLocked(runID string) {
	for i, existing := range d.checkRunOutboxOrder {
		if existing == runID {
			copy(d.checkRunOutboxOrder[i:], d.checkRunOutboxOrder[i+1:])
			d.checkRunOutboxOrder[len(d.checkRunOutboxOrder)-1] = ""
			d.checkRunOutboxOrder = d.checkRunOutboxOrder[:len(d.checkRunOutboxOrder)-1]
			return
		}
	}
}

func (d *Daemon) HandleCheckRunAck(ack *corev1.CheckRunAck) {
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
