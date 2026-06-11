package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gitslice-io/gitslice/internal/storage"
)

type outboxRowError struct {
	id  int64
	err error
}

func (e outboxRowError) Error() string {
	return fmt.Sprintf("outbox row %d failed: %v", e.id, e.err)
}

func (e outboxRowError) Unwrap() error {
	return e.err
}

func (s *ChangesetStore) ProcessOutbox(ctx context.Context, limit int) (storage.OutboxProcessResult, error) {
	if limit <= 0 {
		limit = 64
	}
	var result storage.OutboxProcessResult
	var lastID int64
	for result.Processed+result.Failed < limit {
		id, ok, err := s.processNextOutboxRow(ctx, lastID)
		if !ok {
			if err != nil {
				return result, err
			}
			return result, nil
		}
		lastID = id
		if err != nil {
			var rowErr outboxRowError
			if errors.As(err, &rowErr) {
				result.Failed++
				continue
			}
			return result, err
		}
		result.Processed++
	}
	return result, nil
}

func (s *ChangesetStore) processNextOutboxRow(ctx context.Context, lastID int64) (int64, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	if err := lockOutboxProcessorTx(ctx, tx); err != nil {
		return 0, false, err
	}

	var id int64
	var kind string
	var payload []byte
	err = tx.QueryRowContext(ctx, `
		select id, kind, payload
		from outbox
		where processed_at is null
		  and id > $1
		order by id
		limit 1
		for update skip locked
	`, lastID).Scan(&id, &kind, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}

	if _, err := tx.ExecContext(ctx, `savepoint outbox_apply`); err != nil {
		return id, false, err
	}
	if err := s.applyOutboxRowTx(ctx, tx, kind, payload); err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, `rollback to savepoint outbox_apply`); rollbackErr != nil {
			return id, false, fmt.Errorf("rollback failed outbox row %d apply: %w", id, rollbackErr)
		}
		if _, attemptsErr := tx.ExecContext(ctx, `
			update outbox
			set attempts = attempts + 1
			where id = $1
			  and processed_at is null
		`, id); attemptsErr != nil {
			return id, false, fmt.Errorf("increment outbox attempts after row failure: %w", attemptsErr)
		}
		if err := tx.Commit(); err != nil {
			return id, false, err
		}
		return id, true, outboxRowError{id: id, err: err}
	}
	if _, err := tx.ExecContext(ctx, `release savepoint outbox_apply`); err != nil {
		return id, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		update outbox
		set processed_at = now()
		where id = $1
	`, id); err != nil {
		return id, false, err
	}
	if err := tx.Commit(); err != nil {
		return id, false, err
	}
	return id, true, nil
}

func (s *ChangesetStore) applyOutboxRowTx(ctx context.Context, tx *sql.Tx, kind string, raw []byte) error {
	switch kind {
	case outboxKindCommitPublished:
		var payload commitPublishedPayload
		if err := decodeJSON(raw, &payload); err != nil {
			return err
		}
		return s.applyCommitPublishedOutboxTx(ctx, tx, payload)
	default:
		return fmt.Errorf("%w: unknown outbox kind %q", ErrInvalid, kind)
	}
}

func (s *ChangesetStore) applyCommitPublishedOutboxTx(ctx context.Context, tx *sql.Tx, payload commitPublishedPayload) error {
	if payload.TargetRef == "" || payload.CommitID == "" || payload.BaseCommitID == "" || payload.PatchsetID == "" || payload.CommittedAt.IsZero() {
		return fmt.Errorf("%w: incomplete commit_published payload", ErrInvalid)
	}
	if err := insertCommitChangedPathsTx(ctx, tx, payload.TargetRef, payload.CommitID, payload.ChangedPaths, payload.CommittedAt); err != nil {
		return err
	}
	patchset, err := getPatchsetTx(ctx, tx, payload.PatchsetID)
	if err != nil {
		return err
	}
	return s.applyEntityHistoryTx(ctx, tx, payload.TargetRef, payload.BaseCommitID, payload.CommitID, patchset.FileEdits, payload.CommittedAt)
}

func (s *ChangesetStore) OutboxDepth(ctx context.Context) (int, error) {
	var depth int
	err := s.db.QueryRowContext(ctx, `
		select count(*)
		from outbox
		where processed_at is null
	`).Scan(&depth)
	return depth, err
}

func (s *ChangesetStore) WaitForOutboxDrain(ctx context.Context) error {
	for {
		depth, err := s.OutboxDepth(ctx)
		if err != nil {
			return err
		}
		if depth == 0 {
			return nil
		}
		result, err := s.ProcessOutbox(ctx, min(depth, 128))
		if err != nil {
			return err
		}
		if result.Failed > 0 {
			return fmt.Errorf("outbox drain failed for %d row(s)", result.Failed)
		}
		if result.Processed == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

func (s *ChangesetStore) RebuildDerivedIndexes(ctx context.Context, targetRef string) error {
	if targetRef == "" {
		targetRef = DefaultTargetRef
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockOutboxProcessorTx(ctx, tx); err != nil {
		return err
	}

	var currentCommitID string
	err = tx.QueryRowContext(ctx, `
		select commit_id
		from refs
		where name = $1
		for update
	`, targetRef).Scan(&currentCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := lockTargetOutboxRowsTx(ctx, tx, targetRef); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from commit_changed_paths where target_ref = $1`, targetRef); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from commit_entity_changes where target_ref = $1`, targetRef); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from current_path_entities where target_ref = $1`, targetRef); err != nil {
		return err
	}
	if err := deleteOrphanEntitiesTx(ctx, tx); err != nil {
		return err
	}
	if err := rebuildCommitChangedPathsTx(ctx, tx, targetRef); err != nil {
		return err
	}
	if err := rebuildEntityHistoryTx(ctx, tx, s, targetRef); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update outbox
		set processed_at = now()
		where processed_at is null
		  and kind = $2
		  and payload->>'target_ref' = $1
	`, targetRef, outboxKindCommitPublished); err != nil {
		return err
	}
	return tx.Commit()
}

func lockOutboxProcessorTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `select pg_advisory_xact_lock(724943, 1)`)
	return err
}

func lockTargetOutboxRowsTx(ctx context.Context, tx *sql.Tx, targetRef string) error {
	rows, err := tx.QueryContext(ctx, `
		select id
		from outbox
		where processed_at is null
		  and kind = $2
		  and payload->>'target_ref' = $1
		order by id
		for update
	`, targetRef, outboxKindCommitPublished)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func deleteOrphanEntitiesTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		delete from fs_entities entity
		where not exists (
				select 1
				from current_path_entities current
				where current.account_id = entity.account_id
				  and current.entity_id = entity.entity_id
			)
		  and not exists (
				select 1
				from commit_entity_changes change
				where change.account_id = entity.account_id
				  and change.entity_id = entity.entity_id
			)
	`)
	return err
}

func rebuildCommitChangedPathsTx(ctx context.Context, tx *sql.Tx, targetRef string) error {
	_, err := tx.ExecContext(ctx, `
		with recursive history(commit_id, seen) as (
			select commit_id, array[commit_id]::text[]
			from refs
			where name = $1
			union all
			select parent.id, history.seen || parent.id
			from history
			join commits c on c.id = history.commit_id
			cross join lateral jsonb_array_elements_text(c.parent_ids) as parent(id)
			where not parent.id = any(history.seen)
		)
		insert into commit_changed_paths(target_ref, commit_id, path, committed_at)
		select $1, c.id, changed.path, c.created_at
		from history
		join commits c on c.id = history.commit_id
		cross join lateral jsonb_array_elements_text(c.changed_paths) as changed(path)
		on conflict do nothing
	`, targetRef)
	return err
}

func rebuildEntityHistoryTx(ctx context.Context, tx *sql.Tx, s *ChangesetStore, targetRef string) error {
	rows, err := tx.QueryContext(ctx, `
		with recursive history(commit_id, seen) as (
			select commit_id, array[commit_id]::text[]
			from refs
			where name = $1
			union all
			select parent.id, history.seen || parent.id
			from history
			join commits c on c.id = history.commit_id
			cross join lateral jsonb_array_elements_text(c.parent_ids) as parent(id)
			where not parent.id = any(history.seen)
		)
		select pending.base_ref_commit_id, pending.commit_id, pending.patchset_id, c.created_at
		from history
		join commits c on c.id = history.commit_id
		join pending_publish pending on pending.commit_id = c.id
		where pending.target_ref = $1
		  and pending.status = 'published'
		order by c.created_at, pending.sequence
	`, targetRef)
	if err != nil {
		return err
	}
	defer rows.Close()
	type publishedCommit struct {
		baseCommitID string
		commitID     string
		patchsetID   string
		committedAt  time.Time
	}
	var commits []publishedCommit
	for rows.Next() {
		var commit publishedCommit
		if err := rows.Scan(&commit.baseCommitID, &commit.commitID, &commit.patchsetID, &commit.committedAt); err != nil {
			return err
		}
		commits = append(commits, commit)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, commit := range commits {
		patchset, err := getPatchsetTx(ctx, tx, commit.patchsetID)
		if err != nil {
			return err
		}
		if err := s.applyEntityHistoryTx(ctx, tx, targetRef, commit.baseCommitID, commit.commitID, patchset.FileEdits, commit.committedAt); err != nil {
			return err
		}
	}
	return nil
}
