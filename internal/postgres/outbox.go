package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/storage"
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

const outboxTxBatchLimit = 64

func (s *ChangesetStore) ProcessOutbox(ctx context.Context, limit int) (storage.OutboxProcessResult, error) {
	if limit <= 0 {
		limit = 64
	}
	var result storage.OutboxProcessResult
	var lastID int64
	for result.Processed+result.Failed < limit {
		remaining := limit - (result.Processed + result.Failed)
		chunkLimit := min(remaining, outboxTxBatchLimit)
		step, err := s.processOutboxBatch(ctx, lastID, chunkLimit)
		if err != nil {
			return result, err
		}
		result.Processed += step.processed
		result.Failed += step.failed
		if step.count == 0 {
			return result, nil
		}
		lastID = step.lastID
		if step.count < chunkLimit {
			return result, nil
		}
	}
	return result, nil
}

type outboxBatchStep struct {
	lastID    int64
	count     int
	processed int
	failed    int
}

func (s *ChangesetStore) processOutboxBatch(ctx context.Context, lastID int64, limit int) (outboxBatchStep, error) {
	type rawOutboxRow struct {
		id      int64
		kind    string
		payload []byte
	}
	return inTx(ctx, s.db, func(tx *sql.Tx) (outboxBatchStep, error) {
		if err := lockOutboxProcessorTx(ctx, tx); err != nil {
			return outboxBatchStep{}, err
		}
		rows, err := tx.QueryContext(ctx, `
			select id, kind, payload
			from outbox
			where processed_at is null
			  and id > $1
			order by id
			limit $2
			for update skip locked
		`, lastID, limit)
		if err != nil {
			return outboxBatchStep{}, err
		}
		var batch []rawOutboxRow
		for rows.Next() {
			var row rawOutboxRow
			if err := rows.Scan(&row.id, &row.kind, &row.payload); err != nil {
				_ = rows.Close()
				return outboxBatchStep{}, err
			}
			batch = append(batch, row)
		}
		if err := rows.Close(); err != nil {
			return outboxBatchStep{}, err
		}
		if len(batch) == 0 {
			return outboxBatchStep{}, nil
		}

		step := outboxBatchStep{count: len(batch)}
		succeededIDs := make([]int64, 0, len(batch))
		for _, row := range batch {
			step.lastID = row.id
			if _, err := tx.ExecContext(ctx, `savepoint outbox_apply`); err != nil {
				return step, err
			}
			if err := s.applyOutboxRowTx(ctx, tx, row.kind, row.payload); err != nil {
				if _, rollbackErr := tx.ExecContext(ctx, `rollback to savepoint outbox_apply`); rollbackErr != nil {
					return step, fmt.Errorf("rollback failed outbox row %d apply: %w", row.id, rollbackErr)
				}
				if _, attemptsErr := tx.ExecContext(ctx, `
					update outbox
					set attempts = attempts + 1
					where id = $1
					  and processed_at is null
				`, row.id); attemptsErr != nil {
					return step, fmt.Errorf("increment outbox attempts after row failure: %w", attemptsErr)
				}
				step.failed++
				continue
			}
			if _, err := tx.ExecContext(ctx, `release savepoint outbox_apply`); err != nil {
				return step, err
			}
			succeededIDs = append(succeededIDs, row.id)
			step.processed++
		}
		if len(succeededIDs) > 0 {
			if _, err := tx.ExecContext(ctx, `
				update outbox
				set processed_at = now()
				where id = any($1)
			`, succeededIDs); err != nil {
				return step, err
			}
		}
		return step, nil
	})
}

func (s *ChangesetStore) applyOutboxRowTx(ctx context.Context, tx *sql.Tx, kind string, raw []byte) error {
	switch kind {
	case outboxKindCommitPublished:
		var payload commitPublishedPayload
		if err := decodeJSON(raw, &payload); err != nil {
			return err
		}
		return s.applyCommitPublishedOutboxTx(ctx, tx, payload)
	case outboxKindGitImportBatchPublished:
		var payload gitImportBatchOutboxPayload
		if err := decodeJSON(raw, &payload); err != nil {
			return err
		}
		return s.applyGitImportBatchPublishedOutboxTx(ctx, tx, payload)
	default:
		return fmt.Errorf("%w: unknown outbox kind %q", ErrInvalid, kind)
	}
}

func (s *ChangesetStore) applyCommitPublishedOutboxTx(ctx context.Context, tx *sql.Tx, payload commitPublishedPayload) error {
	if payload.TargetRef == "" || payload.CommitID == "" || payload.BaseCommitID == "" || (payload.PatchsetID == "" && len(payload.FileEdits) == 0 && len(payload.ChangedPaths) > 0) || payload.CommittedAt.IsZero() {
		return fmt.Errorf("%w: incomplete commit_published payload", ErrInvalid)
	}
	if err := insertCommitChangedPathsTx(ctx, tx, payload.TargetRef, payload.CommitID, payload.ChangedPaths, payload.CommittedAt); err != nil {
		return err
	}
	edits := payload.FileEdits
	if payload.PatchsetID != "" {
		patchset, err := getPatchsetTx(ctx, tx, payload.PatchsetID)
		if err != nil {
			return err
		}
		edits = patchset.FileEdits
	}
	if err := s.applyEntityHistoryTx(ctx, tx, payload.TargetRef, payload.BaseCommitID, payload.CommitID, edits, payload.CommittedAt); err != nil {
		return err
	}
	// current_path_entities now reflects payload.CommitID for this ref; advance
	// the materialized-head marker in the same transaction so the read fast path
	// can serve head reads. Without this the marker would only ever be set by the
	// manual RebuildDerivedIndexes admin path and the fast path would never engage
	// in normal operation.
	return setMaterializedHeadTx(ctx, tx, payload.TargetRef, payload.CommitID)
}

func (s *ChangesetStore) applyGitImportBatchPublishedOutboxTx(ctx context.Context, tx *sql.Tx, payload gitImportBatchOutboxPayload) error {
	if payload.TargetRef == "" || payload.CommitID == "" || payload.CommittedAt.IsZero() {
		return fmt.Errorf("%w: incomplete git_import_batch_published payload", ErrInvalid)
	}
	const chunkSize = 5000
	for start := 0; start < len(payload.Deletes); start += chunkSize {
		end := min(start+chunkSize, len(payload.Deletes))
		if _, err := tx.ExecContext(ctx, `
			delete from current_path_entities
			where target_ref = $1 and path = any($2)
		`, payload.TargetRef, payload.Deletes[start:end]); err != nil {
			return err
		}
	}
	for _, prefix := range payload.DeletePrefixes {
		if _, err := tx.ExecContext(ctx, `
			delete from current_path_entities
			where target_ref = $1 and (path = $2 or path like $3 escape '\')
		`, payload.TargetRef, prefix, likePrefixPattern(prefix)); err != nil {
			return err
		}
	}
	for start := 0; start < len(payload.Upserts); start += chunkSize {
		end := min(start+chunkSize, len(payload.Upserts))
		chunk := payload.Upserts[start:end]
		paths := make([]string, len(chunk))
		kinds := make([]string, len(chunk))
		hashes := make([]sql.NullString, len(chunk))
		modes := make([]sql.NullInt32, len(chunk))
		for i, u := range chunk {
			paths[i] = u.Path
			kinds[i] = u.Kind
			if u.Kind == "file" && u.ContentHash != "" {
				hashes[i] = sql.NullString{String: u.ContentHash, Valid: true}
			}
			if u.Kind == "file" && u.Mode != 0 {
				modes[i] = sql.NullInt32{Int32: int32(u.Mode), Valid: true}
			}
		}
		if _, err := tx.ExecContext(ctx, `
			insert into fs_entities(account_id, entity_id, kind, created_commit_id, created_at)
			select accounts.id,
			       'ent_imp_' || left(input.kind, 1) || '_' || md5(input.path),
			       input.kind,
			       $1,
			       $2
			from unnest($3::text[], $4::text[]) as input(path, kind)
			join accounts on accounts.slug = split_part(trim(leading '/' from input.path), '/', 1)
			on conflict (account_id, entity_id) do nothing
		`, payload.CommitID, payload.CommittedAt, paths, kinds); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into current_path_entities(target_ref, path, account_id, entity_id, kind, content_hash, mode, updated_at)
			select $1,
			       input.path,
			       accounts.id,
			       'ent_imp_' || left(input.kind, 1) || '_' || md5(input.path),
			       input.kind,
			       input.content_hash,
			       input.mode,
			       now()
			from unnest($2::text[], $3::text[], $4::text[], $5::integer[])
				as input(path, kind, content_hash, mode)
			join accounts on accounts.slug = split_part(trim(leading '/' from input.path), '/', 1)
			on conflict (target_ref, path) do update
			set account_id = excluded.account_id,
			    entity_id = excluded.entity_id,
			    kind = excluded.kind,
			    content_hash = excluded.content_hash,
			    mode = excluded.mode,
			    updated_at = now()
		`, payload.TargetRef, paths, kinds, hashes, modes); err != nil {
			return err
		}
	}
	return setMaterializedHeadTx(ctx, tx, payload.TargetRef, payload.CommitID)
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
	return execInTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := lockOutboxProcessorTx(ctx, tx); err != nil {
			return err
		}

		var currentCommitID string
		err := tx.QueryRowContext(ctx, `
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
			  and kind in ($2, $3)
			  and payload->>'target_ref' = $1
		`, targetRef, outboxKindCommitPublished, outboxKindGitImportBatchPublished); err != nil {
			return err
		}
		return setMaterializedHeadTx(ctx, tx, targetRef, currentCommitID)
	})
}

// setMaterializedHeadTx records, in the caller's transaction, the commit that
// current_path_entities now reflects for targetRef. The read fast path in
// RepositoryStore.GetFile/GetEntry only serves from the flat index when the
// requested commit equals this marker, so it must be updated in the SAME
// transaction that advances current_path_entities — both the incremental outbox
// path (applyCommitPublishedOutboxTx) and the full RebuildDerivedIndexes rebuild.
func setMaterializedHeadTx(ctx context.Context, tx *sql.Tx, targetRef, commitID string) error {
	_, err := tx.ExecContext(ctx, `
		insert into ref_materialized_heads (target_ref, commit_id, updated_at)
		values ($1, $2, now())
		on conflict (target_ref) do update
		set commit_id = excluded.commit_id,
		    updated_at = now()
	`, targetRef, commitID)
	return err
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
		  and kind in ($2, $3)
		  and payload->>'target_ref' = $1
		order by id
		for update
	`, targetRef, outboxKindCommitPublished, outboxKindGitImportBatchPublished)
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
