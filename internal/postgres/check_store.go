package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

// CheckStore is the PostgreSQL implementation of storage.CheckStore.
type CheckStore struct {
	db *sql.DB
}

func (s *CheckStore) CreateCheckRun(ctx context.Context, in storage.CheckRunInput) (*corev1.CheckRun, error) {
	changesetID := strings.TrimSpace(in.ChangesetID)
	patchsetID := strings.TrimSpace(in.PatchsetID)
	checkName := strings.TrimSpace(in.CheckName)
	daemonID := strings.TrimSpace(in.DaemonID)
	provenance := normalizeCheckRunProvenance(in.Provenance)
	status, err := normalizeCheckRunStatus(in.Status)
	if err != nil {
		return nil, err
	}
	if changesetID == "" {
		return nil, fmt.Errorf("%w: changeset_id is required", storage.ErrInvalid)
	}
	if patchsetID == "" {
		return nil, fmt.Errorf("%w: patchset_id is required", storage.ErrInvalid)
	}
	if checkName == "" {
		return nil, fmt.Errorf("%w: check_name is required", storage.ErrInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var advisoryLock int
	if err := tx.QueryRowContext(ctx, `
		select 1
		from (select pg_advisory_xact_lock(hashtextextended($1 || '/' || $2, 0))) locked
	`, patchsetID, checkName).Scan(&advisoryLock); err != nil {
		return nil, err
	}

	if err := requirePatchsetForChangeset(ctx, tx, changesetID, patchsetID); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		select attempt
		from check_runs
		where patchset_id = $1 and check_name = $2 and superseded_by_run_id is null
		for update
	`, patchsetID, checkName)
	if err != nil {
		return nil, err
	}
	attempt := int32(1)
	for rows.Next() {
		var previousAttempt int32
		if err := rows.Scan(&previousAttempt); err != nil {
			rows.Close()
			return nil, err
		}
		if previousAttempt >= attempt {
			attempt = previousAttempt + 1
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	runID, err := objectid.RandomID("run")
	if err != nil {
		return nil, err
	}
	var (
		startedExpr  = "null"
		finishedExpr = "null"
		durationExpr = "null"
	)
	if status == "running" || isTerminalCheckRunStatus(status) {
		startedExpr = "now()"
	}
	if isTerminalCheckRunStatus(status) {
		finishedExpr = "now()"
		durationExpr = "0"
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		insert into check_runs(id, changeset_id, patchset_id, check_name, daemon_id, provenance, attempt, status, started_at, finished_at, duration_ms)
		values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, %s, %s, %s)
	`, startedExpr, finishedExpr, durationExpr), runID, changesetID, patchsetID, checkName, daemonID, provenance, attempt, status); err != nil {
		return nil, err
	}
	if attempt > 1 {
		if _, err := tx.ExecContext(ctx, `
			update check_runs
			set superseded_by_run_id = $1, updated_at = now()
			where patchset_id = $2 and check_name = $3 and superseded_by_run_id is null and id <> $1
		`, runID, patchsetID, checkName); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetCheckRun(ctx, runID)
}

func (s *CheckStore) GetCheckRun(ctx context.Context, runID string) (*corev1.CheckRun, error) {
	row := s.db.QueryRowContext(ctx, `
		select id, changeset_id, patchset_id, check_name, coalesce(daemon_id, ''), provenance,
		       attempt, status, exit_code, summary, started_at, finished_at, duration_ms, created_at
		from check_runs
		where id = $1
	`, strings.TrimSpace(runID))
	return scanCheckRun(row)
}

func (s *CheckStore) ListCheckRuns(ctx context.Context, changesetID, patchsetID string) ([]*corev1.CheckRun, error) {
	changesetID = strings.TrimSpace(changesetID)
	patchsetID = strings.TrimSpace(patchsetID)
	if changesetID == "" && patchsetID == "" {
		return nil, fmt.Errorf("%w: changeset_id or patchset_id is required", storage.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, changeset_id, patchset_id, check_name, coalesce(daemon_id, ''), provenance,
		       attempt, status, exit_code, summary, started_at, finished_at, duration_ms, created_at
		from check_runs
		where superseded_by_run_id is null
		  and ($1 = '' or changeset_id = $1)
		  and ($2 = '' or patchset_id = $2)
		order by patchset_id asc, check_name asc, attempt asc
	`, changesetID, patchsetID)
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

func (s *CheckStore) ListRunsByDaemonStatus(ctx context.Context, daemonID, status string) ([]*corev1.CheckRun, error) {
	daemonID = strings.TrimSpace(daemonID)
	normalizedStatus, err := normalizeCheckRunStatus(status)
	if err != nil {
		return nil, err
	}
	if daemonID == "" {
		return nil, fmt.Errorf("%w: daemon_id is required", storage.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, changeset_id, patchset_id, check_name, coalesce(daemon_id, ''), provenance,
		       attempt, status, exit_code, summary, started_at, finished_at, duration_ms, created_at
		from check_runs
		where superseded_by_run_id is null
		  and daemon_id = $1
		  and status = $2
		order by created_at asc, id asc
	`, daemonID, normalizedStatus)
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

func (s *CheckStore) CancelOpenCheckRunsBeforePatchset(ctx context.Context, changesetID, currentPatchsetID string) ([]*corev1.CheckRun, error) {
	changesetID = strings.TrimSpace(changesetID)
	currentPatchsetID = strings.TrimSpace(currentPatchsetID)
	if changesetID == "" {
		return nil, fmt.Errorf("%w: changeset_id is required", storage.ErrInvalid)
	}
	if currentPatchsetID == "" {
		return nil, fmt.Errorf("%w: current_patchset_id is required", storage.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := requirePatchsetForChangeset(ctx, tx, changesetID, currentPatchsetID); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		update check_runs
		set status = 'canceled',
		    exit_code = -1,
		    summary = 'superseded by newer patchset',
		    started_at = coalesce(started_at, now()),
		    finished_at = now(),
		    duration_ms = greatest(0, floor(extract(epoch from (now() - coalesce(started_at, now()))) * 1000)::bigint),
		    updated_at = now()
		where changeset_id = $1
		  and patchset_id <> $2
		  and superseded_by_run_id is null
		  and status in ('queued', 'running')
		returning id, changeset_id, patchset_id, check_name, coalesce(daemon_id, ''), provenance,
		          attempt, status, exit_code, summary, started_at, finished_at, duration_ms, created_at
	`, changesetID, currentPatchsetID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *CheckStore) UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error) {
	status, err := normalizeCheckRunStatus(status)
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id is required", storage.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var run struct {
		ChangesetID string
		PatchsetID  string
		CheckName   string
	}
	err = tx.QueryRowContext(ctx, `
		update check_runs
		set status = $2,
		    exit_code = case when $3 then $4 else exit_code end,
		    summary = case when $3 then $5 else summary end,
		    started_at = case when $2 = 'running' or $3 then coalesce(started_at, now()) else started_at end,
		    finished_at = case when $3 then now() else finished_at end,
		    duration_ms = case
		        when $3 then greatest(0, floor(extract(epoch from (now() - coalesce(started_at, now()))) * 1000)::bigint)
		        else duration_ms
		    end,
		    updated_at = now()
		where id = $1
		  and status not in ('passed', 'failed', 'errored', 'skipped', 'canceled')
		  and (superseded_by_run_id is null or $2 = 'canceled')
		returning changeset_id, patchset_id, check_name
	`, runID, status, isTerminalCheckRunStatus(status), exitCode, strings.TrimSpace(summary)).Scan(&run.ChangesetID, &run.PatchsetID, &run.CheckName)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if existsErr := tx.QueryRowContext(ctx, `select exists(select 1 from check_runs where id = $1)`, runID).Scan(&exists); existsErr != nil {
			return nil, existsErr
		}
		if exists {
			return nil, storage.ErrConflict
		}
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if resultStatus, ok := terminalCheckResultStatus(status); ok {
		reportedBy, err := checkRunReporterSubject(ctx, tx, runID)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `
			insert into check_results(changeset_id, patchset_id, check_name, status, reported_by, created_at, updated_at)
			values ($1, $2, $3, $4, $5, now(), now())
			on conflict (changeset_id, patchset_id, check_name) do update
			set status = excluded.status,
			    reported_by = excluded.reported_by,
			    updated_at = now()
		`, run.ChangesetID, run.PatchsetID, run.CheckName, resultStatus, reportedBy)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetCheckRun(ctx, runID)
}

func (s *CheckStore) AppendCheckRunLog(ctx context.Context, runID string, seq int64, stream, chunk string) (bool, error) {
	runID = strings.TrimSpace(runID)
	stream = strings.ToLower(strings.TrimSpace(stream))
	if runID == "" {
		return false, fmt.Errorf("%w: run_id is required", storage.ErrInvalid)
	}
	if seq <= 0 {
		return false, fmt.Errorf("%w: seq must be positive", storage.ErrInvalid)
	}
	if stream != "stdout" && stream != "stderr" {
		return false, fmt.Errorf("%w: stream must be stdout or stderr", storage.ErrInvalid)
	}
	id, err := objectid.RandomID("log")
	if err != nil {
		return false, err
	}
	var insertedID string
	err = s.db.QueryRowContext(ctx, `
		insert into check_run_logs(id, run_id, seq, stream, chunk)
		values ($1, $2, $3, $4, $5)
		on conflict (run_id, seq) do nothing
		returning id
	`, id, runID, seq, stream, chunk).Scan(&insertedID)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `select exists(select 1 from check_runs where id = $1)`, runID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, storage.ErrNotFound
	}
	return false, nil
}

func (s *CheckStore) ListCheckRunLogs(ctx context.Context, runID string, afterSeq int64) ([]*corev1.CheckRunLog, error) {
	rows, err := s.db.QueryContext(ctx, `
		select run_id, seq, stream, chunk, created_at
		from check_run_logs
		where run_id = $1 and seq > $2
		order by seq asc
	`, strings.TrimSpace(runID), afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.CheckRunLog
	for rows.Next() {
		log, err := scanCheckRunLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, log)
	}
	return out, rows.Err()
}

func requirePatchsetForChangeset(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string) error {
	var ok bool
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1 from patchsets where id = $1 and changeset_id = $2
		)
	`, patchsetID, changesetID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return storage.ErrNotFound
	}
	return nil
}

func checkRunReporterSubject(ctx context.Context, tx *sql.Tx, runID string) (string, error) {
	var subjectID sql.NullString
	err := tx.QueryRowContext(ctx, `
		select coalesce(daemon_subject.subject_id, daemon_id_subject.id, changesets.author_subject_id)
		from check_runs
		left join agent_daemons daemon_subject on daemon_subject.id = check_runs.daemon_id
		left join subjects daemon_id_subject on daemon_id_subject.id = check_runs.daemon_id
		left join changesets on changesets.id = check_runs.changeset_id
		where check_runs.id = $1
	`, runID).Scan(&subjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", storage.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !subjectID.Valid || strings.TrimSpace(subjectID.String) == "" {
		return "", storage.ErrNotFound
	}
	return subjectID.String, nil
}

func scanCheckRun(row rowScanner) (*corev1.CheckRun, error) {
	var (
		run                   corev1.CheckRun
		exitCode              sql.NullInt64
		startedAt, finishedAt sql.NullTime
		durationMS            sql.NullInt64
		createdAt             time.Time
	)
	if err := row.Scan(&run.Id, &run.ChangesetId, &run.PatchsetId, &run.CheckName, &run.DaemonId, &run.Provenance, &run.Attempt, &run.Status, &exitCode, &run.Summary, &startedAt, &finishedAt, &durationMS, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	if exitCode.Valid {
		run.ExitCode = int32(exitCode.Int64)
	}
	if startedAt.Valid {
		run.StartedAt = startedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if finishedAt.Valid {
		run.FinishedAt = finishedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if durationMS.Valid {
		run.DurationMs = durationMS.Int64
	}
	run.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return &run, nil
}

func scanCheckRunLog(row rowScanner) (*corev1.CheckRunLog, error) {
	var (
		log       corev1.CheckRunLog
		createdAt time.Time
	)
	if err := row.Scan(&log.RunId, &log.Seq, &log.Stream, &log.Chunk, &createdAt); err != nil {
		return nil, err
	}
	log.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return &log, nil
}

func normalizeCheckRunProvenance(provenance string) string {
	provenance = strings.ToLower(strings.TrimSpace(provenance))
	if provenance == "ci" {
		return provenance
	}
	return "self"
}

func normalizeCheckRunStatus(status string) (string, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "queued"
	}
	switch status {
	case "queued", "running", "passed", "failed", "errored", "skipped", "canceled":
		return status, nil
	default:
		return "", fmt.Errorf("%w: unsupported check run status %q", storage.ErrInvalid, status)
	}
}

func isTerminalCheckRunStatus(status string) bool {
	switch status {
	case "passed", "failed", "errored", "skipped", "canceled":
		return true
	default:
		return false
	}
}

func terminalCheckResultStatus(status string) (string, bool) {
	switch status {
	case "passed":
		return storage.NormalizeCheckStatus(storage.CheckStatusPass)
	case "failed", "errored":
		return storage.NormalizeCheckStatus(storage.CheckStatusFail)
	default:
		return "", false
	}
}
