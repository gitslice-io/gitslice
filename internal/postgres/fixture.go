package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
)

func (d *DB) seedDevFixture(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	parentJSON, err := encodeJSON([]string{})
	if err != nil {
		return err
	}
	changedJSON, err := encodeJSON([]string{})
	if err != nil {
		return err
	}
	rootTreeID := objectid.EmptyTreeID()
	initialCommitID := objectid.CommitID(objectid.CommitObject{
		ParentIDs:    nil,
		RootTreeID:   rootTreeID,
		Author:       "system",
		Message:      "Initial empty tree",
		CreatedAt:    now,
		ChangedPaths: nil,
	})
	if _, err := tx.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at)
		values
			('user_alice', 'user', 'Alice', now()),
			('user_bob', 'user', 'Bob', now()),
			('ci_bot', 'service_account', 'CI Bot', now())
		on conflict (id) do nothing
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into accounts(id, slug, kind, created_at, updated_at)
		values ('acct_acme', 'acme', 'org', now(), now())
		on conflict (id) do nothing
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values
			('acct_acme', 'user_alice', 'admin', now()),
			('acct_acme', 'user_bob', 'writer', now()),
			('acct_acme', 'ci_bot', 'writer', now())
		on conflict do nothing
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
		values ($1, $2, $3, 'system', 'Initial empty tree', $4, $5)
		on conflict (id) do nothing
	`, initialCommitID, parentJSON, rootTreeID, now, changedJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into refs(name, commit_id, version, updated_at, updated_by)
		values ($1, $2, 1, now(), 'system')
		on conflict (name) do nothing
	`, DefaultTargetRef, initialCommitID); err != nil {
		return err
	}
	if err := seedSlice(ctx, tx, "slice_acme_payment", "payment", []string{"/acme/payment"}); err != nil {
		return err
	}
	if err := seedSlice(ctx, tx, "slice_acme_backend", "backend", []string{"/acme/backend", "/acme/payment/shared"}); err != nil {
		return err
	}
	return tx.Commit()
}

func seedSlice(ctx context.Context, tx *sql.Tx, id, slug string, included []string) error {
	includedJSON, err := encodeJSON(included)
	if err != nil {
		return err
	}
	returnSQL := `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, created_at, updated_at)
		values ($1, 'acct_acme', $2, 1, $3, 'account', $4, now(), now())
		on conflict (id) do nothing
	`
	if _, err := tx.ExecContext(ctx, returnSQL, id, slug, definitionHash(id, 1, included, "account", 0, nil), includedJSON); err != nil {
		return err
	}
	return syncSliceIncludedPathsTx(ctx, tx, id, included)
}
