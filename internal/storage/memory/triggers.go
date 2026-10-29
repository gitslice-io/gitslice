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

// TriggerStore keeps trigger runs in memory (design/34_triggers.md).
type TriggerStore struct {
	mu   sync.Mutex
	runs map[string]*storage.TriggerRun
	logs map[string][]storage.TriggerRunLog
}

func NewTriggerStore() *TriggerStore {
	return &TriggerStore{runs: map[string]*storage.TriggerRun{}, logs: map[string][]storage.TriggerRunLog{}}
}

func copyRun(run *storage.TriggerRun) storage.TriggerRun {
	out := *run
	out.State = append([]byte(nil), run.State...)
	return out
}

func (s *TriggerStore) CreateTriggerRuns(ctx context.Context, runs []storage.TriggerRun) ([]storage.TriggerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []storage.TriggerRun
	for _, run := range runs {
		duplicate := false
		for _, existing := range s.runs {
			if existing.Account == run.Account && existing.TriggerID == run.TriggerID && existing.EventID == run.EventID {
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
			run.Status = storage.TriggerRunQueued
		}
		if run.MaxAttempts < 1 {
			run.MaxAttempts = 1
		}
		now := time.Now().UTC()
		run.CreatedAt, run.UpdatedAt, run.NextAttemptAt = now, now, now
		if storage.TriggerRunFinal(run.Status) {
			run.FinishedAt = now
		}
		stored := run
		s.runs[run.ID] = &stored
		out = append(out, copyRun(&stored))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TriggerID < out[j].TriggerID })
	return out, nil
}

func (s *TriggerStore) GetTriggerRun(ctx context.Context, id string) (*storage.TriggerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil {
		return nil, storage.ErrNotFound
	}
	out := copyRun(run)
	return &out, nil
}

func (s *TriggerStore) list(match func(*storage.TriggerRun) bool) []storage.TriggerRun {
	var out []storage.TriggerRun
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

func (s *TriggerStore) ListTriggerRuns(ctx context.Context, f storage.TriggerRunFilter) ([]storage.TriggerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list(func(r *storage.TriggerRun) bool {
		return (f.Account == "" || r.Account == f.Account) && (f.TriggerID == "" || r.TriggerID == f.TriggerID) && (f.ChangesetID == "" || r.ChangesetID == f.ChangesetID)
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

func (s *TriggerStore) ClaimTriggerRun(ctx context.Context, id string, lease time.Duration) (*storage.TriggerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil {
		return nil, storage.ErrNotFound
	}
	if run.Status != storage.TriggerRunQueued {
		return nil, storage.ErrConflict
	}
	now := time.Now().UTC()
	run.Status = storage.TriggerRunRunning
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

func (s *TriggerStore) ExtendTriggerRunLease(ctx context.Context, id string, lease time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil || run.Status != storage.TriggerRunRunning {
		return storage.ErrConflict
	}
	run.LeaseUntil = time.Now().UTC().Add(lease)
	return nil
}

func (s *TriggerStore) UpdateTriggerRun(ctx context.Context, id string, from []string, u storage.TriggerRunUpdate) (*storage.TriggerRun, error) {
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
	if u.Status != storage.TriggerRunRunning {
		run.LeaseUntil = time.Time{}
	}
	if storage.TriggerRunFinal(u.Status) {
		run.FinishedAt = now
	} else {
		run.FinishedAt = time.Time{}
	}
	run.UpdatedAt = now
	out := copyRun(run)
	return &out, nil
}

func (s *TriggerStore) ClaimDueTriggerRuns(ctx context.Context, limit int, hold time.Duration) ([]storage.TriggerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	due := s.list(func(r *storage.TriggerRun) bool {
		return r.Status == storage.TriggerRunQueued && !r.NextAttemptAt.After(now)
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

func (s *TriggerStore) ExpiredTriggerRuns(ctx context.Context, limit int) ([]storage.TriggerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	out := s.list(func(r *storage.TriggerRun) bool {
		return (r.Status == storage.TriggerRunRunning && r.LeaseUntil.Before(now)) ||
			(r.Status == storage.TriggerRunWaiting && !r.Deadline.IsZero() && r.Deadline.Before(now))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *TriggerStore) OpenTriggerRuns(ctx context.Context, account, triggerID, changesetID string) ([]storage.TriggerRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(func(r *storage.TriggerRun) bool {
		return r.Account == account && r.TriggerID == triggerID && (changesetID == "" || r.ChangesetID == changesetID) && !storage.TriggerRunFinal(r.Status)
	}), nil
}

func (s *TriggerStore) CountActiveTriggerRuns(ctx context.Context, account string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list(func(r *storage.TriggerRun) bool {
		return r.Account == account && (r.Status == storage.TriggerRunRunning || r.Status == storage.TriggerRunWaiting)
	})), nil
}

func (s *TriggerStore) SetTriggerRunState(ctx context.Context, id string, state []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[strings.TrimSpace(id)]
	if run == nil || storage.TriggerRunFinal(run.Status) {
		return storage.ErrConflict
	}
	run.State = append([]byte(nil), state...)
	return nil
}

func (s *TriggerStore) AppendTriggerRunLog(ctx context.Context, id, stream, chunk string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if s.runs[id] == nil {
		return 0, storage.ErrNotFound
	}
	seq := int64(len(s.logs[id]) + 1)
	s.logs[id] = append(s.logs[id], storage.TriggerRunLog{RunID: id, Seq: seq, Stream: stream, Chunk: chunk, CreatedAt: time.Now().UTC()})
	return seq, nil
}

func (s *TriggerStore) ListTriggerRunLogs(ctx context.Context, id string, afterSeq int64, limit int) ([]storage.TriggerRunLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []storage.TriggerRunLog
	for _, log := range s.logs[strings.TrimSpace(id)] {
		if log.Seq > afterSeq && (limit <= 0 || len(out) < limit) {
			out = append(out, log)
		}
	}
	return out, nil
}

// HasTriggerFiles is always true in memory, so events are always resolved.
func (s *TriggerStore) HasTriggerFiles(ctx context.Context) (bool, error) {
	return true, nil
}

func (s *TriggerStore) PruneTriggerRuns(ctx context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, run := range s.runs {
		if storage.TriggerRunFinal(run.Status) && run.FinishedAt.Before(before) {
			delete(s.runs, id)
			delete(s.logs, id)
		}
	}
	return nil
}
