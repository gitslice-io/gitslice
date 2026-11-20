package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// AutomationStore keeps trigger runs in memory (design/34_triggers.md).
type AutomationStore struct {
	mu   sync.Mutex
	runs map[string]*storage.AutomationRun
	logs map[string][]storage.AutomationRunLog
}

func NewAutomationStore() *AutomationStore {
	return &AutomationStore{runs: map[string]*storage.AutomationRun{}, logs: map[string][]storage.AutomationRunLog{}}
}

func copyRun(run *storage.AutomationRun) storage.AutomationRun {
	out := *run
	out.State = append([]byte(nil), run.State...)
	return out
}

func (s *AutomationStore) CreateAutomationRuns(ctx context.Context, runs []storage.AutomationRun) ([]storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []storage.AutomationRun
	for _, run := range runs {
		duplicate := false
		for _, existing := range s.runs {
			if existing.Account == run.Account && existing.AutomationID == run.AutomationID && existing.EventID == run.EventID {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if run.ID == "" {
			id, err := objectid.RandomID("trun")
			if err != nil {
				return nil, err
			}
			run.ID = id
		}
		if run.Status == "" {
			run.Status = storage.AutomationRunQueued
		}
		if run.MaxAttempts < 1 {
			run.MaxAttempts = 1
		}
		now := time.Now().UTC()
		run.CreatedAt, run.UpdatedAt, run.NextAttemptAt = now, now, now
		if storage.AutomationRunFinal(run.Status) {
			run.FinishedAt = now
		}
		stored := run
		s.runs[run.ID] = &stored
		out = append(out, copyRun(&stored))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AutomationID < out[j].AutomationID })
	return out, nil
}

func (s *AutomationStore) GetAutomationRun(ctx context.Context, id string) (*storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil {
		return nil, storage.ErrNotFound
	}
	out := copyRun(run)
	return &out, nil
}

func (s *AutomationStore) list(match func(*storage.AutomationRun) bool) []storage.AutomationRun {
	var out []storage.AutomationRun
	for _, run := range s.runs {
		if match(run) {
			out = append(out, copyRun(run))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *AutomationStore) ListAutomationRuns(ctx context.Context, f storage.AutomationRunFilter) ([]storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list(func(r *storage.AutomationRun) bool {
		return (f.Account == "" || r.Account == f.Account) && (f.AutomationID == "" || r.AutomationID == f.AutomationID) && (f.ChangesetID == "" || r.ChangesetID == f.ChangesetID)
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *AutomationStore) ClaimAutomationRun(ctx context.Context, id string, lease time.Duration) (*storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil {
		return nil, storage.ErrNotFound
	}
	if run.Status != storage.AutomationRunQueued {
		return nil, storage.ErrConflict
	}
	now := time.Now().UTC()
	run.Status = storage.AutomationRunRunning
	run.Attempt++
	run.LeaseUntil = now.Add(lease)
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	run.Error = ""
	run.UpdatedAt = now
	out := copyRun(run)
	return &out, nil
}

func (s *AutomationStore) ExtendAutomationRunLease(ctx context.Context, id string, lease time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil || run.Status != storage.AutomationRunRunning {
		return storage.ErrConflict
	}
	run.LeaseUntil = time.Now().UTC().Add(lease)
	return nil
}

func (s *AutomationStore) UpdateAutomationRun(ctx context.Context, id string, from []string, u storage.AutomationRunUpdate) (*storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil {
		return nil, storage.ErrNotFound
	}
	allowed := false
	for _, status := range from {
		if run.Status == status {
			allowed = true
		}
	}
	if !allowed {
		return nil, storage.ErrConflict
	}
	now := time.Now().UTC()
	run.Status = u.Status
	run.Error = u.Error
	if u.ResponseStatus != 0 {
		run.ResponseStatus = u.ResponseStatus
	}
	if !u.NextAttemptAt.IsZero() {
		run.NextAttemptAt = u.NextAttemptAt
	}
	if !u.Deadline.IsZero() {
		run.Deadline = u.Deadline
	}
	if u.Status != storage.AutomationRunRunning {
		run.LeaseUntil = time.Time{}
	}
	if storage.AutomationRunFinal(u.Status) {
		run.FinishedAt = now
	} else {
		run.FinishedAt = time.Time{}
	}
	run.UpdatedAt = now
	out := copyRun(run)
	return &out, nil
}

func (s *AutomationStore) ClaimDueAutomationRuns(ctx context.Context, limit int, hold time.Duration) ([]storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	due := s.list(func(r *storage.AutomationRun) bool {
		return r.Status == storage.AutomationRunQueued && !r.NextAttemptAt.After(now)
	})
	if len(due) > limit {
		due = due[:limit]
	}
	for i := range due {
		run := s.runs[due[i].ID]
		run.NextAttemptAt = now.Add(hold)
		due[i] = copyRun(run)
	}
	return due, nil
}

func (s *AutomationStore) ExpiredAutomationRuns(ctx context.Context, limit int) ([]storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	out := s.list(func(r *storage.AutomationRun) bool {
		return (r.Status == storage.AutomationRunRunning && r.LeaseUntil.Before(now)) ||
			(r.Status == storage.AutomationRunWaiting && !r.Deadline.IsZero() && r.Deadline.Before(now))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *AutomationStore) OpenAutomationRuns(ctx context.Context, account, triggerID, changesetID string) ([]storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(func(r *storage.AutomationRun) bool {
		return r.Account == account && r.AutomationID == triggerID && (changesetID == "" || r.ChangesetID == changesetID) && !storage.AutomationRunFinal(r.Status)
	}), nil
}

func (s *AutomationStore) OpenAutomationRunsInGroup(ctx context.Context, account, group string) ([]storage.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if group == "" {
		return nil, nil
	}
	return s.list(func(r *storage.AutomationRun) bool {
		return r.Account == account && r.ConcurrencyGroup == group && !storage.AutomationRunFinal(r.Status)
	}), nil
}

func (s *AutomationStore) SetAutomationRunDetailsURL(ctx context.Context, id, url string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil || storage.AutomationRunFinal(run.Status) {
		return storage.ErrConflict
	}
	run.DetailsURL = url
	run.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *AutomationStore) CountActiveAutomationRuns(ctx context.Context, account string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list(func(r *storage.AutomationRun) bool {
		return r.Account == account && (r.Status == storage.AutomationRunRunning || r.Status == storage.AutomationRunWaiting)
	})), nil
}

func (s *AutomationStore) CountActiveAutomationRunsByAction(ctx context.Context, account, action string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list(func(r *storage.AutomationRun) bool {
		return r.Account == account && r.Action == action && (r.Status == storage.AutomationRunRunning || r.Status == storage.AutomationRunWaiting)
	})), nil
}

func (s *AutomationStore) SetAutomationRunState(ctx context.Context, id string, state []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil || storage.AutomationRunFinal(run.Status) {
		return storage.ErrConflict
	}
	run.State = append([]byte(nil), state...)
	return nil
}

func (s *AutomationStore) AppendAutomationRunLog(ctx context.Context, id, stream, chunk string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if s.runs[id] == nil {
		return 0, storage.ErrNotFound
	}
	seq := int64(len(s.logs[id]) + 1)
	s.logs[id] = append(s.logs[id], storage.AutomationRunLog{RunID: id, Seq: seq, Stream: stream, Chunk: chunk, CreatedAt: time.Now().UTC()})
	return seq, nil
}

func (s *AutomationStore) ListAutomationRunLogs(ctx context.Context, id string, afterSeq int64, limit int) ([]storage.AutomationRunLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []storage.AutomationRunLog
	for _, log := range s.logs[strings.TrimSpace(id)] {
		if log.Seq > afterSeq && (limit <= 0 || len(out) < limit) {
			out = append(out, log)
		}
	}
	return out, nil
}

// HasAutomationFiles is always true in memory, so events are always resolved.
func (s *AutomationStore) HasAutomationFiles(ctx context.Context) (bool, error) {
	return true, nil
}

func (s *AutomationStore) PruneAutomationRuns(ctx context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, run := range s.runs {
		if storage.AutomationRunFinal(run.Status) && run.FinishedAt.Before(before) {
			delete(s.runs, id)
			delete(s.logs, id)
		}
	}
	return nil
}
