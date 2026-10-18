package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/storage"
)

// Slice members (design/26_slice_members.md): a role on one slice, given to
// someone outside (or inside) the account that owns it.

func (s *AuthStore) SliceRole(ctx context.Context, subjectID, sliceID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `
		select role from slice_members where slice_id = $1 and subject_id = $2
	`, strings.TrimSpace(sliceID), strings.TrimSpace(subjectID)).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// sliceMemberColumns reads a member with their own account's slug and
// profile name, as account members are read.
const sliceMemberColumns = `
	sm.slice_id, sm.subject_id, coalesce(p.slug, ''), coalesce(p.display_name, ''), coalesce(s.kind, ''),
	sm.role, sm.added_by, sm.created_at`

const sliceMemberJoins = `
	left join subjects s on s.id = sm.subject_id
	left join lateral (
		select pa.slug, pa.display_name
		from account_memberships pm
		join accounts pa on pa.id = pm.account_id
		where pa.kind = 'personal' and pm.subject_id = sm.subject_id
		  and not exists (
			select 1 from agent_registrations r
			where r.account_id = pa.id and r.subject_id <> pm.subject_id
		  )
		order by pm.created_at
		limit 1
	) p on true`

func scanSliceMember(row scanner) (*storage.SliceMember, error) {
	var m storage.SliceMember
	if err := row.Scan(&m.SliceID, &m.SubjectID, &m.Username, &m.DisplayName, &m.Kind, &m.Role, &m.AddedBy, &m.CreatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *SliceStore) ListMembers(ctx context.Context, sliceID string) ([]storage.SliceMember, error) {
	rows, err := s.db.QueryContext(ctx, `select `+sliceMemberColumns+` from slice_members sm`+sliceMemberJoins+`
		where sm.slice_id = $1
		order by sm.created_at, sm.subject_id`, strings.TrimSpace(sliceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.SliceMember
	for rows.Next() {
		m, err := scanSliceMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *SliceStore) SetMember(ctx context.Context, sliceID, subjectID, role, addedBy string) (*storage.SliceMember, error) {
	if _, err := s.db.ExecContext(ctx, `
		insert into slice_members(slice_id, subject_id, role, added_by, created_at)
		values ($1, $2, $3, $4, now())
		on conflict (slice_id, subject_id) do update set role = excluded.role
	`, strings.TrimSpace(sliceID), strings.TrimSpace(subjectID), role, addedBy); err != nil {
		return nil, err
	}
	m, err := scanSliceMember(s.db.QueryRowContext(ctx, `select `+sliceMemberColumns+` from slice_members sm`+sliceMemberJoins+`
		where sm.slice_id = $1 and sm.subject_id = $2`, strings.TrimSpace(sliceID), strings.TrimSpace(subjectID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *SliceStore) RemoveMember(ctx context.Context, sliceID, subjectID string) error {
	res, err := s.db.ExecContext(ctx, `delete from slice_members where slice_id = $1 and subject_id = $2`, strings.TrimSpace(sliceID), strings.TrimSpace(subjectID))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SliceStore) ListShared(ctx context.Context, subjectID string) ([]storage.SharedSlice, error) {
	rows, err := s.db.QueryContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''), slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks,
		       slices.archived_at is not null, sm.role
		from slice_members sm
		join slices on slices.id = sm.slice_id
		join accounts on accounts.id = slices.account_id
		where sm.subject_id = $1 and slices.deleted_at is null and slices.archived_at is null
		order by accounts.slug, slices.slug
	`, strings.TrimSpace(subjectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.SharedSlice
	for rows.Next() {
		var role string
		slice, err := scanSlice(roleScanner{row: rows, role: &role})
		if err != nil {
			return nil, err
		}
		out = append(out, storage.SharedSlice{Slice: slice, Role: role})
	}
	return out, rows.Err()
}

// roleScanner lets scanSlice read a slice row that has the member's role
// as one more column.
type roleScanner struct {
	row  scanner
	role *string
}

func (r roleScanner) Scan(dest ...any) error {
	return r.row.Scan(append(dest, r.role)...)
}
