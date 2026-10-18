package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// withFormerNames fills in the names the slice had before it was renamed.
func (s *SliceStore) withFormerNames(ctx context.Context, slice *corev1.Slice) (*corev1.Slice, error) {
	rows, err := s.db.QueryContext(ctx, `select slug from slice_aliases where slice_id = $1 order by slug`, slice.Id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		slice.FormerNames = append(slice.FormerNames, name)
	}
	return slice, rows.Err()
}

// Rename gives the slice a new name. Its earlier name becomes an alias that
// still resolves to it and that no other slice can take; the slice's own
// earlier name can be taken back. The slice id, definition and changesets are
// untouched, apart from the name the changeset tables repeat.
func (s *SliceStore) Rename(ctx context.Context, sliceID, newName string) (*corev1.Slice, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var accountID, account, oldName string
	err = tx.QueryRowContext(ctx, `
		select slices.account_id, accounts.slug, slices.slug
		from slices
		join accounts on accounts.id = slices.account_id
		where slices.id = $1 and slices.deleted_at is null
		for update of slices
	`, sliceID).Scan(&accountID, &account, &oldName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ref, err := normalizeSliceRef(&corev1.SliceRef{Account: account, Slice: newName})
	if err != nil {
		return nil, err
	}
	newName = ref.Slice
	if newName == oldName {
		return nil, fmt.Errorf("%w: slice %s/%s already has that name", ErrInvalid, account, oldName)
	}

	var takenBy string
	err = tx.QueryRowContext(ctx, `select id from slices where account_id = $1 and slug = $2`, accountID, newName).Scan(&takenBy)
	if err == nil {
		return nil, fmt.Errorf("%w: slice %s/%s already exists", ErrConflict, account, newName)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var aliasOf string
	err = tx.QueryRowContext(ctx, `select slice_id from slice_aliases where account_id = $1 and slug = $2`, accountID, newName).Scan(&aliasOf)
	switch {
	case err == nil && aliasOf != sliceID:
		return nil, fmt.Errorf("%w: slice name %s/%s is reserved: another slice was renamed from it", ErrConflict, account, newName)
	case err == nil:
		// Taking back one of its own earlier names.
		if _, err := tx.ExecContext(ctx, `delete from slice_aliases where account_id = $1 and slug = $2`, accountID, newName); err != nil {
			return nil, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `update slices set slug = $2, updated_at = now() where id = $1`, sliceID, newName); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into slice_aliases(account_id, slug, slice_id) values ($1, $2, $3)`, accountID, oldName, sliceID); err != nil {
		return nil, err
	}
	// These tables repeat the slice's name next to its id.
	for _, table := range []string{"changesets", "changeset_stacks", "git_imports"} {
		if _, err := tx.ExecContext(ctx, `update `+table+` set authoring_slice = $2 where authoring_slice_id = $1`, sliceID, newName); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, sliceID)
}

// SetArchived archives the slice or restores it. Setting the state it is
// already in is not an error.
func (s *SliceStore) SetArchived(ctx context.Context, sliceID string, archived bool) (*corev1.Slice, error) {
	res, err := s.db.ExecContext(ctx, `
		update slices
		set archived_at = case when $2 then coalesce(archived_at, now()) else null end,
		    updated_at = now()
		where id = $1 and deleted_at is null
	`, sliceID, archived)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, sliceID)
}

// ChangesetCounts returns how many changesets the slice authored and how many
// of them are still open (neither landed nor abandoned).
func (s *SliceStore) ChangesetCounts(ctx context.Context, sliceID string) (total, open int, err error) {
	err = s.db.QueryRowContext(ctx, `
		select count(*), count(*) filter (where status not in ('submitted', 'abandoned'))
		from changesets
		where authoring_slice_id = $1
	`, sliceID).Scan(&total, &open)
	return total, open, err
}

// Retire removes a slice that has changesets, which Delete refuses to do. The
// row stays so the changesets and the name stay valid, but the slice no
// longer resolves or lists, covers no paths and has no secrets. It fails if
// any changeset is still open.
func (s *SliceStore) Retire(ctx context.Context, sliceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var live bool
	if err := tx.QueryRowContext(ctx, `select deleted_at is null from slices where id = $1 for update`, sliceID).Scan(&live); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if !live {
		return ErrNotFound
	}
	var open int
	if err := tx.QueryRowContext(ctx, `
		select count(*) from changesets where authoring_slice_id = $1 and status not in ('submitted', 'abandoned')
	`, sliceID).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return fmt.Errorf("%w: slice has %d open changeset(s)", ErrConflict, open)
	}
	for _, stmt := range []string{
		`delete from slice_included_paths where slice_id = $1`,
		`delete from slice_secrets where slice_id = $1`,
		`update slices set deleted_at = now(), updated_at = now() where id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, sliceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResolveRetired finds a retired slice by the name it had.
func (s *SliceStore) ResolveRetired(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''), slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks,
		       slices.archived_at is not null
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug = $2 and slices.deleted_at is not null
	`, ref.Account, ref.Slice)
	return scanSlice(row)
}
