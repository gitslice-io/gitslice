package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// SetCIDaemons sets the slice's pool of CI daemons, in order of preference
// (design/32 layer 3); slices.ci_daemon_id is the first of them, for older
// readers. An empty list clears it.
func (s *SliceStore) SetCIDaemons(ctx context.Context, sliceID string, daemonIDs []string) (*corev1.Slice, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	daemonIDs = storage.NormalizeCIDaemons(daemonIDs)
	first := ""
	if len(daemonIDs) > 0 {
		first = daemonIDs[0]
	}
	if err := execInTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			update slices
			set ci_daemon_id = nullif($2, ''),
			    updated_at = now()
			where id = $1
		`, sliceID, first)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `delete from slice_ci_daemons where slice_id = $1`, sliceID); err != nil {
			return err
		}
		for i, daemonID := range daemonIDs {
			if _, err := tx.ExecContext(ctx, `
				insert into slice_ci_daemons(slice_id, daemon_id, position) values ($1, $2, $3)
			`, sliceID, daemonID, i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.Get(ctx, sliceID)
}

func (s *SliceStore) ListByCIDaemon(ctx context.Context, daemonIDs []string) ([]*corev1.Slice, error) {
	if len(daemonIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select `+sliceSelectColumns+`
		from slices
		join accounts on accounts.id = slices.account_id
		where exists (select 1 from slice_ci_daemons p where p.slice_id = slices.id and p.daemon_id = any($1))
		  and slices.deleted_at is null
		order by accounts.slug, slices.slug
	`, daemonIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.Slice
	for rows.Next() {
		slice, err := scanSlice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, slice)
	}
	return out, rows.Err()
}

func (s *SliceStore) AccountCIDaemons(ctx context.Context, account string) ([]string, []string, error) {
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" {
		return nil, nil, fmt.Errorf("%w: account is required", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		select p.daemon_id, coalesce(nullif(d.label, ''), d.name, p.daemon_id)
		from account_ci_daemons p
		join accounts a on a.id = p.account_id
		left join agent_daemons d on d.id = p.daemon_id
		where a.slug = $1
		order by p.position, p.added_at
	`, account)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids, names []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		names = append(names, name)
	}
	return ids, names, rows.Err()
}

func (s *SliceStore) SetAccountCIDaemons(ctx context.Context, account string, daemonIDs []string) ([]string, []string, error) {
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" {
		return nil, nil, fmt.Errorf("%w: account is required", ErrInvalid)
	}
	daemonIDs = storage.NormalizeCIDaemons(daemonIDs)
	if err := execInTx(ctx, s.db, func(tx *sql.Tx) error {
		var accountID string
		err := tx.QueryRowContext(ctx, `select id from accounts where slug = $1`, account).Scan(&accountID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from account_ci_daemons where account_id = $1`, accountID); err != nil {
			return err
		}
		for i, daemonID := range daemonIDs {
			if _, err := tx.ExecContext(ctx, `
				insert into account_ci_daemons(account_id, daemon_id, position) values ($1, $2, $3)
			`, accountID, daemonID, i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return s.AccountCIDaemons(ctx, account)
}

func (s *SliceStore) ListAccountsByCIDaemon(ctx context.Context, daemonIDs []string) (map[string][]string, error) {
	if len(daemonIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select p.daemon_id, a.slug
		from account_ci_daemons p
		join accounts a on a.id = p.account_id
		where p.daemon_id = any($1)
		order by a.slug
	`, daemonIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var daemonID, slug string
		if err := rows.Scan(&daemonID, &slug); err != nil {
			return nil, err
		}
		out[daemonID] = append(out[daemonID], slug)
	}
	return out, rows.Err()
}
