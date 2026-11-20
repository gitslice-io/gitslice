package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	checkDispatchSweepInterval = 10 * time.Second
	checkDispatchSweepMinAge   = 15 * time.Second
	// checkRecoverEvery is how many sweeps pass between looks for runs whose
	// process went away or whose daemon is gone (recoverCheckRuns).
	checkRecoverEvery = 3
)

// strandedRunAfter is how long a queued run waits on a daemon that is gone
// before it moves to another daemon of the slice's pool.
var strandedRunAfter = 2 * time.Minute

// replayDaemonCheckRuns sends a process that (re)connected the runs it
// holds claims on, then the daemon's unclaimed queued runs it has room for.
func (s *AgentService) replayDaemonCheckRuns(ctx context.Context, daemonID string, conn *daemonConn) {
	if s.Checks == nil || s.dispatcher == nil || conn == nil {
		return
	}
	if claimed, err := s.Checks.ListClaimedCheckRuns(ctx, conn.instance); err == nil {
		s.dispatcher.sendRuns(ctx, conn, claimed, false)
	}
	s.dispatcher.dispatchQueued(ctx, daemonID)
}

func (s *AgentService) RunCheckDispatchSweep(ctx context.Context) {
	if s == nil || s.hub == nil || s.Checks == nil || s.dispatcher == nil {
		return
	}
	ticker := time.NewTicker(checkDispatchSweepInterval)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// An idle server leaves the database alone (internal/activity).
			if activity.Default.Wait(ctx) != nil {
				return
			}
			s.runCheckDispatchSweepOnce(ctx, now)
			if tick%checkRecoverEvery == 0 {
				s.dispatcher.recoverCheckRuns(ctx)
			}
		}
	}
}

// runCheckDispatchSweepOnce sends each connected process again the queued
// runs it holds that it has not started (a message may have been lost), and
// hands out queued runs nobody holds.
func (s *AgentService) runCheckDispatchSweepOnce(ctx context.Context, now time.Time) {
	if s == nil || s.hub == nil || s.Checks == nil || s.dispatcher == nil {
		return
	}
	for _, daemonID := range s.hub.onlineDaemonIDs() {
		for _, conn := range s.hub.conns(daemonID) {
			if err := ctx.Err(); err != nil {
				return
			}
			runs, err := s.Checks.ListClaimedCheckRuns(ctx, conn.instance)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("check dispatch sweep failed to list claimed runs", "daemon_id", daemonID, "error", err)
				continue
			}
			if stale := staleCheckRunsForDispatch(runs, now); len(stale) > 0 {
				s.dispatcher.sendRuns(ctx, conn, stale, true)
			}
		}
		s.dispatcher.dispatchQueued(ctx, daemonID)
	}
}

func staleCheckRunsForDispatch(runs []*corev1.CheckRun, now time.Time) []*corev1.CheckRun {
	stale := make([]*corev1.CheckRun, 0, len(runs))
	for _, run := range runs {
		if run == nil || run.Provenance != "ci" || run.Status != "queued" {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339Nano, run.GetCreatedAt())
		if err != nil {
			slog.Warn("check dispatch sweep skipped run with invalid created_at", "run_id", run.GetId(), "created_at", run.GetCreatedAt(), "error", err)
			continue
		}
		if now.Sub(createdAt) < checkDispatchSweepMinAge {
			continue
		}
		stale = append(stale, run)
	}
	return stale
}

// recoverCheckRuns looks after runs whose daemon process or daemon went
// away (design/32 layer 3): a queued run whose claim lapsed is handed out
// again, a running one is tried again (up to storage.MaxCheckRunAttempts),
// and a queued run whose daemon is gone moves to another daemon of the pool.
func (d *checkDispatcher) recoverCheckRuns(ctx context.Context) {
	if d == nil || d.Checks == nil {
		return
	}
	lapsed, err := d.Checks.TakeLapsedCheckRuns(ctx, 100)
	if err != nil {
		slog.Warn("failed to take lapsed check run claims", "error", err)
	}
	for _, run := range lapsed {
		if run.GetStatus() == "running" {
			d.retryLostRun(ctx, run)
			continue
		}
		d.handOut(ctx, run.GetDaemonId())
	}
	// Runners exit when idle; queued runs that waited for a free one get
	// another ring.
	for daemonID := range d.onDemand {
		d.handOut(ctx, daemonID)
	}
	stranded, err := d.Checks.ListStrandedCheckRuns(ctx, strandedRunAfter)
	if err != nil {
		slog.Warn("failed to list stranded check runs", "error", err)
	}
	for _, run := range stranded {
		d.moveStrandedRun(ctx, run)
	}
}

// runContext loads a run's changeset, patchset and slice.
func (d *checkDispatcher) runContext(ctx context.Context, run *corev1.CheckRun) (*corev1.Changeset, *corev1.Patchset, *corev1.Slice, error) {
	cs, err := d.Changesets.Get(ctx, run.GetChangesetId())
	if err != nil {
		return nil, nil, nil, err
	}
	patchset := patchsetByID(cs, run.GetPatchsetId())
	if patchset == nil {
		return nil, nil, nil, storage.ErrNotFound
	}
	slice, err := d.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, nil, nil, err
	}
	return cs, patchset, slice, nil
}

// retryLostRun ends a running run whose daemon process stopped responding,
// and queues another attempt on the slice's pool unless it has had its
// tries.
func (d *checkDispatcher) retryLostRun(ctx context.Context, run *corev1.CheckRun) {
	lost := "the CI daemon process running it stopped responding"
	if run.GetAttempt() >= storage.MaxCheckRunAttempts {
		if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.GetId(), "errored", -1, fmt.Sprintf("%s (tried %d times)", lost, run.GetAttempt())); err != nil && !errors.Is(err, storage.ErrConflict) {
			slog.Warn("failed to end a lost check run", "run_id", run.GetId(), "error", err)
		}
		return
	}
	_, _, slice, err := d.runContext(ctx, run)
	if err != nil {
		slog.Warn("failed to load a lost check run's slice", "run_id", run.GetId(), "error", err)
		return
	}
	daemonID, refusal := d.pickDaemon(ctx, slice)
	if daemonID == "" || refusal != "" {
		if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.GetId(), "errored", -1, lost+"; "+firstNonEmptyString(refusal, "the slice has no CI daemon to try it again")); err != nil && !errors.Is(err, storage.ErrConflict) {
			slog.Warn("failed to end a lost check run", "run_id", run.GetId(), "error", err)
		}
		return
	}
	next, err := d.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID:     run.GetChangesetId(),
		PatchsetID:      run.GetPatchsetId(),
		CheckName:       run.GetCheckName(),
		DaemonID:        daemonID,
		Provenance:      "ci",
		Status:          "queued",
		AutomationRunID: run.GetAutomationRunId(),
	})
	if err != nil {
		slog.Warn("failed to queue another attempt of a lost check run", "run_id", run.GetId(), "error", err)
		return
	}
	if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.GetId(), "canceled", -1, fmt.Sprintf("%s; tried again as attempt %d", lost, next.GetAttempt())); err != nil && !errors.Is(err, storage.ErrConflict) {
		slog.Warn("failed to end a lost check run", "run_id", run.GetId(), "error", err)
	}
	d.cancelCheckRun(run)
	d.handOut(ctx, daemonID)
}

// moveStrandedRun moves a queued run whose daemon is gone to another daemon
// of the slice's pool that is online.
func (d *checkDispatcher) moveStrandedRun(ctx context.Context, run *corev1.CheckRun) {
	_, _, slice, err := d.runContext(ctx, run)
	if err != nil {
		return
	}
	daemonID, refusal := d.pickDaemon(ctx, slice)
	if daemonID == "" || refusal != "" || daemonID == run.GetDaemonId() || !d.daemonReachable(ctx, daemonID) {
		return
	}
	moved, err := d.Checks.ReassignCheckRun(ctx, run.GetId(), run.GetDaemonId(), daemonID)
	if err != nil {
		slog.Warn("failed to move a stranded check run", "run_id", run.GetId(), "error", err)
		return
	}
	if moved {
		slog.Info("moved a check run off a daemon that is gone", "run_id", run.GetId(), "from", run.GetDaemonId(), "to", daemonID)
		d.handOut(ctx, daemonID)
	}
}

func (d *checkDispatcher) rebuildRunChecksMessage(ctx context.Context, runs []*corev1.CheckRun) (*corev1.ServerMessage, error) {
	if d == nil || d.Changesets == nil || d.Slices == nil {
		return nil, fmt.Errorf("check dispatcher is not configured")
	}
	first := runs[0]
	cs, err := d.Changesets.Get(ctx, first.ChangesetId)
	if err != nil {
		return nil, err
	}
	patchset := patchsetByID(cs, first.PatchsetId)
	if patchset == nil {
		return nil, storage.ErrNotFound
	}
	slice, err := d.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, err
	}
	// Queued runs for a daemon that is no longer authorized for the slice or
	// account are not sent to it: they error, and the daemon never sees secrets.
	if refusal := d.ciDaemonRefusalForSlice(ctx, slice, first.DaemonId); refusal != "" {
		for _, run := range runs {
			if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.Id, "errored", -1, refusal); err != nil {
				slog.Warn("failed to mark queued ci check run errored for an unexposed daemon", "run_id", run.Id, "error", err)
			}
		}
		return nil, nil
	}
	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return nil, err
	}
	byName := map[string]checks.CheckSpec{}
	for _, spec := range append(append([]checks.CheckSpec(nil), plan.Runnable...), plan.Triggered...) {
		byName[spec.Name] = spec
	}
	var queued []checks.CheckSpec
	for _, run := range runs {
		if spec, ok := byName[run.CheckName]; ok {
			queued = append(queued, spec)
		}
	}
	var secrets map[string]map[string]string
	if d.patchsetApprovedForGitsliceSecrets(ctx, cs, slice, patchset) {
		secrets, err = d.secretsForChecks(ctx, slice.GetRef().GetAccount(), queued)
		if err != nil {
			return nil, err
		}
	}
	specs := make([]*corev1.CheckRunSpec, 0, len(runs))
	for _, run := range runs {
		spec, ok := byName[run.CheckName]
		if !ok {
			continue
		}
		specs = append(specs, checkRunSpecFromPlan(run.Id, spec, secrets[spec.Name]))
	}
	if len(specs) == 0 {
		return nil, nil
	}
	return &corev1.ServerMessage{Payload: &corev1.ServerMessage_RunChecks{RunChecks: &corev1.RunChecks{
		ChangesetId:  first.ChangesetId,
		PatchsetId:   first.PatchsetId,
		ResultTreeId: patchset.ResultTreeId,
		Slice:        slice.Ref,
		SliceId:      slice.Id,
		ServerAddr:   d.serverAddr,
		Checks:       specs,
	}}}, nil
}

func (s *AgentService) handleCheckRunUpdate(ctx context.Context, daemonID string, conn *daemonConn, update *corev1.CheckRunUpdate) {
	if s.Checks == nil || conn == nil || update == nil {
		return
	}
	ack := func() {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_CheckAck{CheckAck: &corev1.CheckRunAck{
			RunId:          update.RunId,
			AckedClientSeq: update.ClientSeq,
		}}})
	}
	run, err := s.Checks.GetCheckRun(ctx, update.RunId)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			conn.untrackRun(update.RunId)
			ack()
		}
		return
	}
	if run.DaemonId != daemonID {
		conn.untrackRun(update.RunId)
		ack()
		return
	}
	if !update.Final && !isFinalCheckStatus(update.Status) && (run.Status == "queued" || run.Status == "running") {
		conn.trackRun(run.Id)
	} else {
		conn.untrackRun(run.Id)
	}
	// The process reporting on a run holds it (design/32 layer 3). A run
	// that ended without it (canceled, or tried again elsewhere after its
	// claim lapsed) is stopped there.
	if run.Status == "queued" || run.Status == "running" {
		_ = s.Checks.RenewCheckRunClaim(ctx, run.Id, conn.instance, storage.CheckRunLease)
	} else if update.Status != "" && update.Status != run.Status && !update.Final {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_CancelCheck{CancelCheck: &corev1.CancelCheckRun{RunId: run.Id}}})
	}
	if s.dispatcher != nil && (update.Final || isFinalCheckStatus(update.Status)) {
		// A slot is free on that process.
		defer s.dispatcher.dispatchQueued(context.WithoutCancel(ctx), daemonID)
	}
	if update.LogChunk != "" || update.Summary != "" {
		masker := s.runSecretMasker(ctx, run)
		update.LogChunk = masker.Mask(update.LogChunk)
		update.Summary = masker.Mask(update.Summary)
	}
	if update.LogChunk != "" && update.ClientSeq > 0 {
		streamName := defaultCheckStream(update.Stream)
		inserted, err := s.Checks.AppendCheckRunLog(ctx, update.RunId, update.ClientSeq, streamName, update.LogChunk)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) {
				ack()
			}
			return
		}
		if inserted && s.checkLogs != nil {
			log := &corev1.CheckRunLog{
				RunId:  update.RunId,
				Seq:    update.ClientSeq,
				Stream: streamName,
				Chunk:  update.LogChunk,
			}
			if stored := s.persistedCheckRunLog(ctx, update.RunId, update.ClientSeq); stored != nil {
				log = stored
			}
			s.checkLogs.publish(update.RunId, log)
		}
	}
	if update.Status != "" {
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, update.RunId, update.Status, update.ExitCode, update.Summary); err != nil {
			if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) {
				ack()
			}
			return
		}
		if s.checkLogs != nil {
			s.checkLogs.publish(update.RunId, nil)
		}
	} else if update.Final {
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, update.RunId, "errored", update.ExitCode, update.Summary); err != nil {
			if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) {
				ack()
			}
			return
		}
		if s.checkLogs != nil {
			s.checkLogs.publish(update.RunId, nil)
		}
	}
	ack()
}

func (s *AgentService) persistedCheckRunLog(ctx context.Context, runID string, seq int64) *corev1.CheckRunLog {
	logs, err := s.Checks.ListCheckRunLogs(ctx, runID, seq-1)
	if err != nil {
		return nil
	}
	for _, log := range logs {
		if log.GetSeq() == seq {
			return log
		}
	}
	return nil
}
