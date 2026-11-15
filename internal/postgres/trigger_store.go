package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// TriggerStore keeps trigger runs (design/34_triggers.md).
type TriggerStore struct {
	db *sql.DB
}

const triggerRunColumns = `trigger_runs.id, accounts.slug, trigger_runs.trigger_id, trigger_runs.folder, trigger_runs.action,
	trigger_runs.event_id, trigger_runs.event_kind, trigger_runs.changeset_id, trigger_runs.patchset_id, trigger_runs.commit_id,
	trigger_runs.target_ref, trigger_runs.definition_commit_id, trigger_runs.spec, trigger_runs.payload, trigger_runs.state,
	trigger_runs.status, trigger_runs.attempt, trigger_runs.max_attempts, trigger_runs.error, trigger_runs.response_status,
	trigger_runs.lease_until, trigger_runs.next_attempt_at, trigger_runs.deadline, trigger_runs.created_at,
	trigger_runs.started_at, trigger_runs.finished_at, trigger_runs.updated_at`

const triggerRunFrom = ` from trigger_runs join accounts on accounts.id = trigger_runs.account_id `

func scanTriggerRun(row rowScanner) (*storage.TriggerRun, error) {
	var run storage.TriggerRun
	var state []byte
	var lease, started, finished, deadline sql.NullTime
	if err := row.Scan(&run.ID, &run.Account, &run.TriggerID, &run.Folder, &run.Action,
		&run.EventID, &run.EventKind, &run.ChangesetID, &run.PatchsetID, &run.CommitID,
		&run.TargetRef, &run.DefinitionCommitID, &run.Spec, &run.Payload, &state,
		&run.Status, &run.Attempt, &run.MaxAttempts, &run.Error, &run.ResponseStatus,
		&lease, &run.NextAttemptAt, &deadline, &run.CreatedAt,
		&started, &finished, &run.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	run.State = state
	run.LeaseUntil = lease.Time
	run.StartedAt = started.Time
	run.FinishedAt = finished.Time
	run.Deadline = deadline.Time
	return &run, nil
}

func (s *TriggerStore) queryRuns(ctx context.Context, q secretQueryer, query string, args ...any) ([]storage.TriggerRun, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.TriggerRun
	for rows.Next() {
		run, err := scanTriggerRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func (s *TriggerStore) CreateTriggerRuns(ctx context.Context, runs []storage.TriggerRun) ([]storage.TriggerRun, error) {
	if len(runs) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var ids []string
	for _, run := range runs {
		id := run.ID
		if id == "" {
			if id, err = objectid.RandomID("trun"); err != nil {
				return nil, err
			}
		}
		status := run.Status
		if status == "" {
			status = storage.TriggerRunQueued
		}
		maxAttempts := run.MaxAttempts
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		var state any
		if len(run.State) > 0 {
			state = run.State
		}
		res, err := tx.ExecContext(ctx, `
			insert into trigger_runs(id, account_id, trigger_id, folder, action, event_id, event_kind, changeset_id, patchset_id,
				commit_id, target_ref, definition_commit_id, spec, payload, state, status, max_attempts, error, finished_at)
			select $1, accounts.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
				case when $16 in ('succeeded', 'failed', 'errored', 'canceled', 'timed_out') then now() end
			from accounts where accounts.slug = $2
			on conflict (account_id, trigger_id, event_id) do nothing
		`, id, run.Account, run.TriggerID, run.Folder, run.Action, run.EventID, run.EventKind, run.ChangesetID, run.PatchsetID,
			run.CommitID, run.TargetRef, run.DefinitionCommitID, jsonOrEmpty(run.Spec), jsonOrEmpty(run.Payload), state, status, maxAttempts, run.Error)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			ids = append(ids, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return s.queryRuns(ctx, s.db, `select `+triggerRunColumns+triggerRunFrom+`where trigger_runs.id = any($1) order by trigger_runs.trigger_id`, ids)
}

func jsonOrEmpty(data []byte) []byte {
	if len(data) == 0 {
		return []byte("{}")
	}
	return data
}

func (s *TriggerStore) GetTriggerRun(ctx context.Context, id string) (*storage.TriggerRun, error) {
	return scanTriggerRun(s.db.QueryRowContext(ctx, `select `+triggerRunColumns+triggerRunFrom+`where trigger_runs.id = $1`, strings.TrimSpace(id)))
}

func (s *TriggerStore) ListTriggerRuns(ctx context.Context, f storage.TriggerRunFilter) ([]storage.TriggerRun, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.queryRuns(ctx, s.db, `select `+triggerRunColumns+triggerRunFrom+`
		where ($1 = '' or accounts.slug = $1)
		  and ($2 = '' or trigger_runs.trigger_id = $2)
		  and ($3 = '' or trigger_runs.changeset_id = $3)
		order by trigger_runs.created_at desc, trigger_runs.id desc
		limit $4`, f.Account, f.TriggerID, f.ChangesetID, limit)
}

func (s *TriggerStore) ClaimTriggerRun(ctx context.Context, id string, lease time.Duration) (*storage.TriggerRun, error) {
	row := s.db.QueryRowContext(ctx, `
		with claimed as (
			update trigger_runs
			set status = 'running', attempt = attempt + 1, lease_until = now() + $2::interval,
			    started_at = coalesce(started_at, now()), error = '', updated_at = now()
			where id = $1 and status = 'queued'
			returning *
		)
		select `+strings.ReplaceAll(triggerRunColumns, "trigger_runs.", "claimed.")+`
		from claimed join accounts on accounts.id = claimed.account_id
	`, strings.TrimSpace(id), interval(lease))
	run, err := scanTriggerRun(row)
	if errors.Is(err, storage.ErrNotFound) {
		if _, getErr := s.GetTriggerRun(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, storage.ErrConflict
	}
	return run, err
}

func (s *TriggerStore) ExtendTriggerRunLease(ctx context.Context, id string, lease time.Duration) error {
	res, err := s.db.ExecContext(ctx, `
		update trigger_runs set lease_until = now() + $2::interval, updated_at = now()
		where id = $1 and status = 'running'
	`, strings.TrimSpace(id), interval(lease))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrConflict
	}
	return nil
}

func (s *TriggerStore) UpdateTriggerRun(ctx context.Context, id string, from []string, u storage.TriggerRunUpdate) (*storage.TriggerRun, error) {
	var next, deadline any
	if !u.NextAttemptAt.IsZero() {
		next = u.NextAttemptAt
	}
	if !u.Deadline.IsZero() {
		deadline = u.Deadline
	}
	row := s.db.QueryRowContext(ctx, `
		with updated as (
			update trigger_runs
			set status = $3,
			    error = $4,
			    response_status = case when $5 <> 0 then $5 else response_status end,
			    next_attempt_at = coalesce($6, next_attempt_at),
			    deadline = coalesce($7, deadline),
			    lease_until = case when $3 = 'running' then lease_until end,
			    finished_at = case when $3 in ('succeeded', 'failed', 'errored', 'canceled', 'timed_out') then now() end,
			    updated_at = now()
			where id = $1 and status = any($2)
			returning *
		)
		select `+strings.ReplaceAll(triggerRunColumns, "trigger_runs.", "updated.")+`
		from updated join accounts on accounts.id = updated.account_id
	`, strings.TrimSpace(id), from, u.Status, u.Error, u.ResponseStatus, next, deadline)
	run, err := scanTriggerRun(row)
	if errors.Is(err, storage.ErrNotFound) {
		if _, getErr := s.GetTriggerRun(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, storage.ErrConflict
	}
	return run, err
}

func (s *TriggerStore) ClaimDueTriggerRuns(ctx context.Context, limit int, hold time.Duration) ([]storage.TriggerRun, error) {
	return s.queryRuns(ctx, s.db, `
		with due as (
			select id from trigger_runs
			where status = 'queued' and next_attempt_at <= now()
			order by next_attempt_at, created_at
			limit $1
			for update skip locked
		), held as (
			update trigger_runs set next_attempt_at = now() + $2::interval, updated_at = now()
			where id in (select id from due)
			returning *
		)
		select `+strings.ReplaceAll(triggerRunColumns, "trigger_runs.", "held.")+`
		from held join accounts on accounts.id = held.account_id
		order by held.created_at`, limit, interval(hold))
}

func (s *TriggerStore) ExpiredTriggerRuns(ctx context.Context, limit int) ([]storage.TriggerRun, error) {
	return s.queryRuns(ctx, s.db, `select `+triggerRunColumns+triggerRunFrom+`
		where (trigger_runs.status = 'running' and trigger_runs.lease_until < now())
		   or (trigger_runs.status = 'waiting' and trigger_runs.deadline < now())
		order by trigger_runs.updated_at
		limit $1`, limit)
}

func (s *TriggerStore) OpenTriggerRuns(ctx context.Context, account, triggerID, changesetID string) ([]storage.TriggerRun, error) {
	return s.queryRuns(ctx, s.db, `select `+triggerRunColumns+triggerRunFrom+`
		where accounts.slug = $1 and trigger_runs.trigger_id = $2
		  and ($3 = '' or trigger_runs.changeset_id = $3)
		  and trigger_runs.status in ('queued', 'running', 'waiting')
		order by trigger_runs.created_at`, account, triggerID, changesetID)
}

func (s *TriggerStore) CountActiveTriggerRuns(ctx context.Context, account string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		select count(*) from trigger_runs join accounts on accounts.id = trigger_runs.account_id
		where accounts.slug = $1 and trigger_runs.status in ('running', 'waiting')
	`, account).Scan(&n)
	return n, err
}

func (s *TriggerStore) SetTriggerRunState(ctx context.Context, id string, state []byte) error {
	var value any
	if len(state) > 0 {
		value = state
	}
	res, err := s.db.ExecContext(ctx, `
		update trigger_runs set state = $2, updated_at = now()
		where id = $1 and status in ('queued', 'running', 'waiting')
	`, strings.TrimSpace(id), value)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrConflict
	}
	return nil
}

func (s *TriggerStore) AppendTriggerRunLog(ctx context.Context, id, stream, chunk string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		insert into trigger_run_logs(run_id, seq, stream, chunk)
		select $1, coalesce(max(seq), 0) + 1, $2, $3 from trigger_run_logs where run_id = $1
		returning seq
	`, strings.TrimSpace(id), stream, chunk).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("append trigger run log: %w", err)
	}
	return seq, nil
}

func (s *TriggerStore) ListTriggerRunLogs(ctx context.Context, id string, afterSeq int64, limit int) ([]storage.TriggerRunLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		select run_id, seq, stream, chunk, created_at from trigger_run_logs
		where run_id = $1 and seq > $2 order by seq limit $3
	`, strings.TrimSpace(id), afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.TriggerRunLog
	for rows.Next() {
		var log storage.TriggerRunLog
		if err := rows.Scan(&log.RunID, &log.Seq, &log.Stream, &log.Chunk, &log.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, log)
	}
	return out, rows.Err()
}

func (s *TriggerStore) HasTriggerFiles(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		select exists(select 1 from current_path_entities where path like '%/.gitslice/triggers.yaml')
	`).Scan(&exists)
	return exists, err
}

func (s *TriggerStore) PruneTriggerRuns(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		delete from trigger_runs
		where status in ('succeeded', 'failed', 'errored', 'canceled', 'timed_out') and finished_at < $1
	`, before)
	return err
}

// interval renders a duration for a ::interval cast.
func interval(d time.Duration) string {
	return fmt.Sprintf("%d milliseconds", d.Milliseconds())
}
