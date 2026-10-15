package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *CheckStore) CreateCheckRun(ctx context.Context, in storage.CheckRunInput) (*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	changesetID := strings.TrimSpace(in.ChangesetID)
	patchsetID := strings.TrimSpace(in.PatchsetID)
	checkName := strings.TrimSpace(in.CheckName)
	status, err := normalizeMemoryCheckRunStatus(in.Status)
	if err != nil {
		return nil, err
	}
	if changesetID == "" || patchsetID == "" || checkName == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil || !changesetHasPatchsetID(cs, patchsetID) {
		return nil, storage.ErrNotFound
	}
	attempt := int32(1)
	for _, existing := range s.b.checkRuns {
		if existing.PatchsetId != patchsetID || existing.CheckName != checkName {
			continue
		}
		if s.b.checkRunSupersededBy[existing.Id] != "" {
			continue
		}
		if existing.Attempt >= attempt {
			attempt = existing.Attempt + 1
		}
	}
	id, err := objectid.RandomID("run")
	if err != nil {
		return nil, err
	}
	provenance := strings.ToLower(strings.TrimSpace(in.Provenance))
	if provenance != "ci" {
		provenance = "self"
	}
	now := nowRFC()
	run := &corev1.CheckRun{
		Id:          id,
		ChangesetId: changesetID,
		PatchsetId:  patchsetID,
		CheckName:   checkName,
		DaemonId:    strings.TrimSpace(in.DaemonID),
		Provenance:  provenance,
		Attempt:     attempt,
		Status:      status,
		Summary:     "",
		CreatedAt:   now,
	}
	if status == "running" || isTerminalMemoryCheckRunStatus(status) {
		run.StartedAt = now
	}
	if isTerminalMemoryCheckRunStatus(status) {
		run.FinishedAt = now
		run.DurationMs = 0
	}
	s.b.checkRuns[id] = run
	for _, existing := range s.b.checkRuns {
		if existing.Id == id || existing.PatchsetId != patchsetID || existing.CheckName != checkName {
			continue
		}
		if s.b.checkRunSupersededBy[existing.Id] == "" {
			s.b.checkRunSupersededBy[existing.Id] = id
		}
	}
	return cloneCheckRun(run), nil
}

func (s *CheckStore) GetCheckRun(ctx context.Context, runID string) (*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	run := s.b.checkRuns[strings.TrimSpace(runID)]
	if run == nil {
		return nil, storage.ErrNotFound
	}
	return cloneCheckRun(run), nil
}

func (s *CheckStore) ListCheckRuns(ctx context.Context, changesetID, patchsetID string) ([]*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	changesetID = strings.TrimSpace(changesetID)
	patchsetID = strings.TrimSpace(patchsetID)
	if changesetID == "" && patchsetID == "" {
		return nil, storage.ErrInvalid
	}
	var out []*corev1.CheckRun
	for _, run := range s.b.checkRuns {
		if s.b.checkRunSupersededBy[run.Id] != "" {
			continue
		}
		if changesetID != "" && run.ChangesetId != changesetID {
			continue
		}
		if patchsetID != "" && run.PatchsetId != patchsetID {
			continue
		}
		out = append(out, cloneCheckRun(run))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PatchsetId != out[j].PatchsetId {
			return out[i].PatchsetId < out[j].PatchsetId
		}
		if out[i].CheckName != out[j].CheckName {
			return out[i].CheckName < out[j].CheckName
		}
		return out[i].Attempt < out[j].Attempt
	})
	return out, nil
}

func (s *CheckStore) ListRunsByDaemonStatus(ctx context.Context, daemonID, status string) ([]*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	daemonID = strings.TrimSpace(daemonID)
	status, err := normalizeMemoryCheckRunStatus(status)
	if err != nil {
		return nil, err
	}
	if daemonID == "" {
		return nil, storage.ErrInvalid
	}
	var out []*corev1.CheckRun
	for _, run := range s.b.checkRuns {
		if s.b.checkRunSupersededBy[run.Id] != "" {
			continue
		}
		if run.DaemonId != daemonID || run.Status != status {
			continue
		}
		out = append(out, cloneCheckRun(run))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].Id < out[j].Id
	})
	return out, nil
}

func (s *CheckStore) CancelOpenCheckRunsBeforePatchset(ctx context.Context, changesetID, currentPatchsetID string) ([]*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	changesetID = strings.TrimSpace(changesetID)
	currentPatchsetID = strings.TrimSpace(currentPatchsetID)
	if changesetID == "" || currentPatchsetID == "" {
		return nil, storage.ErrInvalid
	}
	cs := s.b.changesets[changesetID]
	if cs == nil || !changesetHasPatchsetID(cs, currentPatchsetID) {
		return nil, storage.ErrNotFound
	}
	now := nowRFC()
	var out []*corev1.CheckRun
	for _, run := range s.b.checkRuns {
		if run.ChangesetId != changesetID || run.PatchsetId == currentPatchsetID {
			continue
		}
		if s.b.checkRunSupersededBy[run.Id] != "" {
			continue
		}
		if run.Status != "queued" && run.Status != "running" {
			continue
		}
		run.Status = "canceled"
		if run.StartedAt == "" {
			run.StartedAt = now
		}
		run.FinishedAt = now
		run.DurationMs = memoryCheckRunDurationMS(run.StartedAt, now)
		run.ExitCode = -1
		run.Summary = "superseded by newer patchset"
		out = append(out, cloneCheckRun(run))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].Id < out[j].Id
	})
	return out, nil
}

func (s *CheckStore) UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	status, err := normalizeMemoryCheckRunStatus(status)
	if err != nil {
		return nil, err
	}
	run := s.b.checkRuns[strings.TrimSpace(runID)]
	if run == nil {
		return nil, storage.ErrNotFound
	}
	if isTerminalMemoryCheckRunStatus(run.Status) {
		return nil, storage.ErrConflict
	}
	if s.b.checkRunSupersededBy[run.Id] != "" && status != "canceled" {
		return nil, storage.ErrConflict
	}
	now := nowRFC()
	run.Status = status
	if status == "running" && run.StartedAt == "" {
		run.StartedAt = now
	}
	if isTerminalMemoryCheckRunStatus(status) {
		if run.StartedAt == "" {
			run.StartedAt = now
		}
		run.FinishedAt = now
		run.DurationMs = memoryCheckRunDurationMS(run.StartedAt, now)
		run.ExitCode = exitCode
		run.Summary = strings.TrimSpace(summary)
	}
	if resultStatus, ok := terminalMemoryCheckResultStatus(status); ok {
		key := patchsetRequirementKey(run.ChangesetId, run.PatchsetId)
		if s.b.checkResults[key] == nil {
			s.b.checkResults[key] = map[string]string{}
		}
		s.b.checkResults[key][run.CheckName] = resultStatus
		if cs := s.b.changesets[run.ChangesetId]; cs != nil {
			clearRequirementBlockLocked(cs, run.PatchsetId)
		}
	}
	return cloneCheckRun(run), nil
}

func (s *CheckStore) AppendCheckRunLog(ctx context.Context, runID string, seq int64, stream, chunk string) (bool, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	runID = strings.TrimSpace(runID)
	stream = strings.ToLower(strings.TrimSpace(stream))
	if runID == "" || seq <= 0 {
		return false, storage.ErrInvalid
	}
	if stream != "stdout" && stream != "stderr" {
		return false, storage.ErrInvalid
	}
	if s.b.checkRuns[runID] == nil {
		return false, storage.ErrNotFound
	}
	for _, existing := range s.b.checkRunLogs[runID] {
		if existing.Seq == seq {
			return false, nil
		}
	}
	log := &corev1.CheckRunLog{
		RunId:     runID,
		Seq:       seq,
		Stream:    stream,
		Chunk:     chunk,
		CreatedAt: nowRFC(),
	}
	s.b.checkRunLogs[runID] = append(s.b.checkRunLogs[runID], log)
	return true, nil
}

func (s *CheckStore) ListCheckRunLogs(ctx context.Context, runID string, afterSeq int64) ([]*corev1.CheckRunLog, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.CheckRunLog
	for _, log := range s.b.checkRunLogs[strings.TrimSpace(runID)] {
		if log.Seq > afterSeq {
			out = append(out, cloneCheckRunLog(log))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func normalizeMemoryCheckRunStatus(status string) (string, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "queued"
	}
	switch status {
	case "queued", "running", "passed", "failed", "errored", "skipped", "canceled":
		return status, nil
	default:
		return "", storage.ErrInvalid
	}
}

func isTerminalMemoryCheckRunStatus(status string) bool {
	switch status {
	case "passed", "failed", "errored", "skipped", "canceled":
		return true
	default:
		return false
	}
}

func terminalMemoryCheckResultStatus(status string) (string, bool) {
	switch status {
	case "passed":
		return storage.NormalizeCheckStatus(storage.CheckStatusPass)
	case "failed", "errored":
		return storage.NormalizeCheckStatus(storage.CheckStatusFail)
	default:
		return "", false
	}
}

func memoryCheckRunDurationMS(startedAt, finishedAt string) int64 {
	start, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return 0
	}
	finish, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil || finish.Before(start) {
		return 0
	}
	return finish.Sub(start).Milliseconds()
}
