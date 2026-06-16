package postgres

import (
	"context"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
)

// seedBootstrap inserts the minimum state a fresh database needs to function: the
// initial empty-tree commit and the global ref pointing at it. It is idempotent
// (on conflict do nothing). No accounts, subjects, or slices are seeded — those
// are created at runtime via authenticated provisioning (ChooseUsername).
func (d *DB) seedBootstrap(ctx context.Context) error {
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
	return tx.Commit()
}
