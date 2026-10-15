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

// insertSubmittedWebhookEventsTx records a changeset.submitted webhook event
// for each changeset the batch published, in the publishing transaction, so
// an event exists exactly when the commit does (design/24_webhooks.md).
func insertSubmittedWebhookEventsTx(ctx context.Context, tx *sql.Tx, build *pendingPublishBuild) error {
	byCommit := map[string]commitPublishedPayload{}
	for _, payload := range build.OutboxPayloads {
		byCommit[payload.CommitID] = payload
	}
	for _, update := range build.ChangesetUpdates {
		published := byCommit[update.CommitID]
		if _, err := appendWebhookEvent(ctx, tx, storage.WebhookEvent{
			Kind:         storage.WebhookEventChangesetSubmitted,
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
