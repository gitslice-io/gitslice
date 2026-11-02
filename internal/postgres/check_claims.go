package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Claims on check runs (design/32_ci_dispatch_on_cloudflare.md, layer 3). A
// claim names the daemon process (storage.InstanceKey) a queued run was sent
// to, and lapses unless that process keeps showing signs of life.

const checkRunColumns = `id, changeset_id, patchset_id, check_name, coalesce(daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = check_runs.daemon_id), ''), provenance,
		       attempt, status, exit_code, summary, started_at, finished_at, duration_ms, created_at, coalesce(trigger_run_id, ''), details_url, annotations`

func leaseInterval(lease time.Duration) string {
	if lease <= 0 {
		lease = storage.CheckRunLease
	}
	return interval(lease)
}

func (s *CheckStore) runsByID(ctx context.Context, ids []string) ([]*corev1.CheckRun, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `select `+checkRunColumns+` from check_runs where id = any($1) order by created_at asc, id asc`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.CheckRun
	for rows.Next() {
		run, err := scanCheckRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func collectIDs(ctx context.Context, s *CheckStore, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *CheckStore) ClaimCheckRuns(ctx context.Context, daemonID, instance string, limit int, lease time.Duration) ([]*corev1.CheckRun, error) {
	daemonID, instance = strings.TrimSpace(daemonID), strings.TrimSpace(instance)
	if daemonID == "" || instance == "" {
		return nil, fmt.Errorf("%w: daemon_id and instance are required", storage.ErrInvalid)
	}
	if limit <= 0 {
		limit = 1000
	}
	ids, err := collectIDs(ctx, s, `
		update check_runs
		set claimed_by = $2, lease_expires_at = now() + $4::interval, updated_at = now()
		where id in (
			select id from check_runs
			where daemon_id = $1 and status = 'queued' and superseded_by_run_id is null
			  and (claimed_by is null or lease_expires_at < now())
			order by created_at asc, id asc
			limit $3
			for update skip locked
		)
		returning id
	`, daemonID, instance, limit, leaseInterval(lease))
	if err != nil {
		return nil, err
	}
	return s.runsByID(ctx, ids)
}

func (s *CheckStore) ListClaimedCheckRuns(ctx context.Context, instance string) ([]*corev1.CheckRun, error) {
	ids, err := collectIDs(ctx, s, `
		select id from check_runs
		where claimed_by = $1 and status in ('queued', 'running') and superseded_by_run_id is null
	`, strings.TrimSpace(instance))
	if err != nil {
		return nil, err
	}
	return s.runsByID(ctx, ids)
}

func (s *CheckStore) RenewCheckRunClaim(ctx context.Context, runID, instance string, lease time.Duration) error {
	_, err := s.db.ExecContext(ctx, `
		update check_runs
		set claimed_by = $2, lease_expires_at = now() + $3::interval
		where id = $1 and status in ('queued', 'running')
	`, strings.TrimSpace(runID), strings.TrimSpace(instance), leaseInterval(lease))
	return err
}

func (s *CheckStore) RenewCheckRunClaims(ctx context.Context, instance string, lease time.Duration) error {
	_, err := s.db.ExecContext(ctx, `
		update check_runs
		set lease_expires_at = now() + $2::interval
		where claimed_by = $1 and status in ('queued', 'running')
	`, strings.TrimSpace(instance), leaseInterval(lease))
	return err
}

func (s *CheckStore) ReleaseCheckRunClaims(ctx context.Context, instance string) error {
	_, err := s.db.ExecContext(ctx, `
		update check_runs
		set claimed_by = null, lease_expires_at = null
		where claimed_by = $1 and status = 'queued'
	`, strings.TrimSpace(instance))
	return err
}

func (s *CheckStore) CountUnclaimedCheckRuns(ctx context.Context, daemonID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		select count(*) from check_runs
		where daemon_id = $1 and status = 'queued' and superseded_by_run_id is null
		  and (claimed_by is null or lease_expires_at < now())
	`, strings.TrimSpace(daemonID)).Scan(&n)
	return n, err
}

func (s *CheckStore) HasPendingCheckRuns(ctx context.Context, daemonID, instance string) (bool, error) {
	var pending bool
	err := s.db.QueryRowContext(ctx, `
		select exists(
			select 1 from check_runs
			where daemon_id = $1 and superseded_by_run_id is null
			  and ((status = 'queued' and (claimed_by is null or claimed_by = $2 or lease_expires_at < now()))
			    or (status = 'running' and claimed_by = $2))
		)
	`, strings.TrimSpace(daemonID), strings.TrimSpace(instance)).Scan(&pending)
	return pending, err
}

func (s *CheckStore) TakeLapsedCheckRuns(ctx context.Context, limit int) ([]*corev1.CheckRun, error) {
	if limit <= 0 {
		limit = 100
	}
	ids, err := collectIDs(ctx, s, `
		update check_runs
		set claimed_by = null, lease_expires_at = null
		where id in (
			select id from check_runs
			where lease_expires_at < now() and status in ('queued', 'running')
			order by lease_expires_at asc
			limit $1
			for update skip locked
		)
		returning id
	`, limit)
	if err != nil {
		return nil, err
	}
	return s.runsByID(ctx, ids)
}

func (s *CheckStore) ListStrandedCheckRuns(ctx context.Context, minAge time.Duration) ([]*corev1.CheckRun, error) {
	ids, err := collectIDs(ctx, s, `
		select check_runs.id from check_runs
		left join agent_daemons d on d.id = check_runs.daemon_id
		where check_runs.status = 'queued' and check_runs.claimed_by is null
		  and check_runs.superseded_by_run_id is null and check_runs.daemon_id is not null
		  and check_runs.created_at < now() - $1::interval
		  and (d.id is null or d.status <> 'online'
		    or ((d.last_seen_at is null or d.last_seen_at < now() - $2::interval)
		        and (d.standby_until is null or d.standby_until < now())))
		limit 200
	`, interval(minAge), interval(storage.DaemonStaleAfter))
	if err != nil {
		return nil, err
	}
	return s.runsByID(ctx, ids)
}

func (s *CheckStore) ReassignCheckRun(ctx context.Context, runID, fromDaemon, toDaemon string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		update check_runs
		set daemon_id = $3, updated_at = now()
		where id = $1 and daemon_id = $2 and status = 'queued' and claimed_by is null
	`, strings.TrimSpace(runID), strings.TrimSpace(fromDaemon), strings.TrimSpace(toDaemon))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
