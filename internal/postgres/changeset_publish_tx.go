package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

func resetPendingPublishClaimsTx(ctx context.Context, tx *sql.Tx, pendingIDs []string) error {
	if len(pendingIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		update pending_publish
		set status = 'pending', updated_at = now()
		where id = any($1) and status = 'publishing'
	`, pendingIDs)
	return err
}

func lockPendingPublishChangesetsTx(ctx context.Context, tx *sql.Tx, updates []submittedChangesetUpdate) error {
	changesetIDs := make([]string, 0, len(updates))
	updatesByID := make(map[string]submittedChangesetUpdate, len(updates))
	for _, update := range updates {
		changesetIDs = append(changesetIDs, update.ChangesetID)
		updatesByID[update.ChangesetID] = update
	}
	rows, err := tx.QueryContext(ctx, `
		select id, status, updated_at
		from changesets
		where id = any($1)
		order by id
		for update
	`, changesetIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var changesetID, status string
		var updatedAt time.Time
		if err := rows.Scan(&changesetID, &status, &updatedAt); err != nil {
			return err
		}
		seen++
		if status != "pending_publish" || !updatedAt.Equal(updatesByID[changesetID].ObservedUpdatedAt) {
			return ErrConflict
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen != len(changesetIDs) {
		return ErrNotFound
	}
	return nil
}

func insertPublishedCommitsTx(ctx context.Context, tx *sql.Tx, rows []publishCommitInsert) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
		values `)
	args := make([]any, 0, len(rows)*7)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d)", base, base+1, base+2, base+3, base+4, base+5, base+6))
		args = append(args, row.ID, row.ParentIDsJSON, row.RootTreeID, row.AuthorSubjectID, row.Message, row.CreatedAt, row.ChangedJSON)
	}
	b.WriteString(" on conflict (id) do nothing")
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func insertCommitPublishedOutboxTx(ctx context.Context, tx *sql.Tx, payloads []commitPublishedPayload) error {
	if len(payloads) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		insert into outbox(kind, payload, created_at)
		values `)
	args := make([]any, 0, len(payloads)*3)
	for i, payload := range payloads {
		raw, err := encodeJSON(payload)
		if err != nil {
			return err
		}
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d, $%d)", base, base+1, base+2))
		args = append(args, outboxKindCommitPublished, raw, payload.CommittedAt)
	}
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func markPendingPublishedTx(ctx context.Context, tx *sql.Tx, rows []publishedPendingUpdate) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		update pending_publish as pending
		set status = 'published',
		    commit_id = published.commit_id,
		    updated_at = now(),
		    published_at = now()
		from (values `)
	args := make([]any, 0, len(rows)*2)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d)", base, base+1))
		args = append(args, row.PendingID, row.CommitID)
	}
	b.WriteString(`
		) as published(id, commit_id)
		where pending.id = published.id`)
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func markPendingFailedTx(ctx context.Context, tx *sql.Tx, rows []failedPendingUpdate) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		update pending_publish as pending
		set status = 'failed',
		    error = failed.error,
		    updated_at = now()
		from (values `)
	args := make([]any, 0, len(rows)*2)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d)", base, base+1))
		args = append(args, row.PendingID, row.Error)
	}
	b.WriteString(`
		) as failed(id, error)
		where pending.id = failed.id and pending.status = 'publishing'`)
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func reconcileFailedPathHeadsTx(ctx context.Context, tx *sql.Tx, rows []failedPathHeadReconcile) error {
	for _, row := range rows {
		var blobID, contentHash, mode, size any
		if row.Head.Exists {
			if row.Head.BlobID != "" {
				blobID = row.Head.BlobID
			}
			if row.Head.ContentHash != "" {
				contentHash = row.Head.ContentHash
			}
			if row.Head.Mode != 0 {
				mode = int64(row.Head.Mode)
			}
			if row.Head.Size != 0 || row.Head.BlobID != "" {
				size = row.Head.Size
			}
		}
		if _, err := tx.ExecContext(ctx, `
			update path_heads
			set exists = $3,
			    entry_fingerprint = $4,
			    blob_id = $5,
			    content_hash = $6,
			    mode = $7,
			    size = $8,
			    accepted_changeset_id = null,
			    accepted_patchset_id = null,
			    updated_at = now()
			where path = $1 and accepted_changeset_id = $2
		`, row.Head.Path, row.ChangesetID, row.Head.Exists, row.Head.EntryFingerprint, blobID, contentHash, mode, size); err != nil {
			return err
		}
	}
	return nil
}

func markChangesetsSubmittedTx(ctx context.Context, tx *sql.Tx, rows []submittedChangesetUpdate) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		update changesets as changeset
		set status = 'submitted',
		    commit_id = published.commit_id,
		    updated_at = now()
		from (values `)
	args := make([]any, 0, len(rows)*2)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d)", base, base+1))
		args = append(args, row.ChangesetID, row.CommitID)
	}
	b.WriteString(`
		) as published(id, commit_id)
		where changeset.id = published.id`)
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

func insertCommitChangedPathsTx(ctx context.Context, tx *sql.Tx, targetRef, commitID string, changedPaths []string, committedAt time.Time) error {
	if len(changedPaths) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
		insert into commit_changed_paths(target_ref, commit_id, path, committed_at)
		values `)
	args := make([]any, 0, len(changedPaths)*4)
	for i, p := range changedPaths {
		if i > 0 {
			b.WriteString(", ")
		}
		base := len(args) + 1
		b.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d)", base, base+1, base+2, base+3))
		args = append(args, targetRef, commitID, p, committedAt)
	}
	b.WriteString(" on conflict do nothing")
	_, err := tx.ExecContext(ctx, b.String(), args...)
	return err
}

// insertSubmittedEventsTx records a changeset.submitted event for each
// changeset the batch published, in the publishing transaction, so an event
// exists exactly when the commit does.
func insertSubmittedEventsTx(ctx context.Context, tx *sql.Tx, build *pendingPublishBuild) error {
	byCommit := map[string]commitPublishedPayload{}
	for _, payload := range build.OutboxPayloads {
		byCommit[payload.CommitID] = payload
	}
	for _, update := range build.ChangesetUpdates {
		published := byCommit[update.CommitID]
		if _, err := appendEvent(ctx, tx, storage.Event{
			Kind:         storage.EventChangesetSubmitted,
			ChangesetID:  update.ChangesetID,
			PatchsetID:   published.PatchsetID,
			CommitID:     update.CommitID,
			TargetRef:    published.TargetRef,
			ChangedPaths: published.ChangedPaths,
		}); err != nil {
			return err
		}
	}
	return nil
}
