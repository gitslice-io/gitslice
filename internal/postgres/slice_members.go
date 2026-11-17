package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	role = strings.ToLower(strings.TrimSpace(role))
	if storage.SliceRoleRank(role) >= len(storage.SliceRoles) {
		return nil, fmt.Errorf("%w: invalid slice role %q", ErrInvalid, role)
	}
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
		select `+sliceSelectColumns+`, r.role
		from (
			select distinct on (slice_id) slice_id, role
			from (
				select sm.slice_id, sm.role
				from slice_members sm
				where sm.subject_id = $1
				union all
				select sgm.slice_id, sgm.role
				from slice_group_members sgm
				join account_group_members agm on agm.group_id = sgm.group_id
				where agm.subject_id = $1
			) combined
			order by slice_id, case role
				when 'admin' then 1
				when 'writer' then 2
				when 'contributor' then 3
				when 'reader' then 4
				else 5
			end
		) r
		join slices on slices.id = r.slice_id
		join accounts on accounts.id = slices.account_id
		where slices.deleted_at is null and slices.archived_at is null
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

const sliceGroupMemberColumns = `
	sgm.slice_id, sgm.group_id, a.slug, g.name, sgm.role, sgm.added_by, sgm.created_at,
	(select count(*) from account_group_members agm where agm.group_id = sgm.group_id)`

func scanSliceGroupMember(row scanner) (*storage.SliceGroupMember, error) {
	var m storage.SliceGroupMember
	if err := row.Scan(&m.SliceID, &m.GroupID, &m.Account, &m.Name, &m.Role, &m.AddedBy, &m.CreatedAt, &m.MemberCount); err != nil {
		return nil, err
	}
	m.Handle = storage.FormatGroupHandle(m.Account, m.Name)
	return &m, nil
}

func (s *SliceStore) ListGroupMembers(ctx context.Context, sliceID string) ([]storage.SliceGroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+sliceGroupMemberColumns+`
		from slice_group_members sgm
		join account_groups g on g.id = sgm.group_id
		join accounts a on a.id = g.account_id
		where sgm.slice_id = $1
		order by a.slug, g.name
	`, strings.TrimSpace(sliceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.SliceGroupMember
	for rows.Next() {
		m, err := scanSliceGroupMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *SliceStore) SetGroupMember(ctx context.Context, sliceID, groupID, role, addedBy string) (*storage.SliceGroupMember, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if storage.SliceRoleRank(role) >= len(storage.SliceRoles) {
		return nil, fmt.Errorf("%w: invalid slice role %q", ErrInvalid, role)
	}
	sliceID = strings.TrimSpace(sliceID)
	groupID = strings.TrimSpace(groupID)
	if _, err := s.db.ExecContext(ctx, `
		insert into slice_group_members(slice_id, group_id, role, added_by, created_at, updated_at)
		values ($1, $2, $3, $4, now(), now())
		on conflict (slice_id, group_id) do update
		set role = excluded.role,
		    updated_at = now()
	`, sliceID, groupID, role, addedBy); err != nil {
		return nil, err
	}
	m, err := scanSliceGroupMember(s.db.QueryRowContext(ctx, `
		select `+sliceGroupMemberColumns+`
		from slice_group_members sgm
		join account_groups g on g.id = sgm.group_id
		join accounts a on a.id = g.account_id
		where sgm.slice_id = $1 and sgm.group_id = $2
	`, sliceID, groupID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *SliceStore) RemoveGroupMember(ctx context.Context, sliceID, groupID string) error {
	res, err := s.db.ExecContext(ctx, `
		delete from slice_group_members where slice_id = $1 and group_id = $2
	`, strings.TrimSpace(sliceID), strings.TrimSpace(groupID))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
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
