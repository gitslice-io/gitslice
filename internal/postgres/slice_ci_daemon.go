package postgres

import (
	"context"
	"fmt"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *SliceStore) SetCIDaemon(ctx context.Context, sliceID, daemonID string) (*corev1.Slice, error) {
	sliceID = strings.TrimSpace(sliceID)
	daemonID = strings.TrimSpace(daemonID)
	if sliceID == "" {
		return nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx, `
		update slices
		set ci_daemon_id = nullif($2, ''),
		    updated_at = now()
		where id = $1
	`, sliceID, daemonID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, sliceID)
}

func (s *SliceStore) ListByCIDaemon(ctx context.Context, daemonIDs []string) ([]*corev1.Slice, error) {
	if len(daemonIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''), slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks,
		       slices.archived_at is not null, slices.description, slices.merge_on_submit
		from slices
		join accounts on accounts.id = slices.account_id
		where slices.ci_daemon_id = any($1) and slices.deleted_at is null
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
