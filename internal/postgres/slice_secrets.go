package postgres

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
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

func (s *SliceStore) SetSliceSecret(ctx context.Context, sliceID, name, value string) error {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if !storage.ValidSliceSecretName(name) {
		return fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
	}
	if err := s.requireSliceExists(ctx, sliceID); err != nil {
		return err
	}
	// Configured servers write enc:v1 envelope-encrypted values. Reads retain a
	// legacy-plaintext path so existing rows can be migrated on their next write.
	if s.Secrets != nil {
		sealed, err := s.Secrets.Seal(value)
		if err != nil {
			return fmt.Errorf("seal slice secret: %w", err)
		}
		value = sealed
	}
	_, err := s.db.ExecContext(ctx, `
		insert into slice_secrets(slice_id, name, value, created_at, updated_at)
		values ($1, $2, $3, now(), now())
		on conflict (slice_id, name) do update
		set value = excluded.value,
		    updated_at = now()
	`, sliceID, name, value)
	return err
}

func (s *SliceStore) DeleteSliceSecret(ctx context.Context, sliceID, name string) error {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if !storage.ValidSliceSecretName(name) {
		return fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
	}
	if err := s.requireSliceExists(ctx, sliceID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `delete from slice_secrets where slice_id = $1 and name = $2`, sliceID, name)
	return err
}

func (s *SliceStore) ListSliceSecretNames(ctx context.Context, sliceID string) ([]string, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if err := s.requireSliceExists(ctx, sliceID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		select name
		from slice_secrets
		where slice_id = $1
		order by name
	`, sliceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *SliceStore) GetSliceSecrets(ctx context.Context, sliceID string) (map[string]string, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if err := s.requireSliceExists(ctx, sliceID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		select name, value
		from slice_secrets
		where slice_id = $1
	`, sliceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		if s.Secrets == nil {
			if secretbox.IsSealed(value) {
				return nil, fmt.Errorf("slice secret %q is encrypted but no secrets key is configured", name)
			}
		} else {
			value, err = s.Secrets.Open(value)
			if err != nil {
				return nil, fmt.Errorf("open slice secret %q: %w", name, err)
			}
		}
		out[name] = value
	}
	return out, rows.Err()
}
