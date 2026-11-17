package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

const groupSubjectPersonalJoinSQL = `
	left join subjects ms on ms.id = agm.subject_id
	left join lateral (
		select pa.slug, pa.display_name
		from account_memberships pm
		join accounts pa on pa.id = pm.account_id
		where pa.kind = 'personal' and pm.subject_id = agm.subject_id
		  and not exists (
			select 1 from agent_registrations r
			where r.account_id = pa.id and r.subject_id <> pm.subject_id
		  )
		order by pm.created_at
		limit 1
	) mp on true
	left join lateral (
		select pa.slug
		from account_memberships pm
		join accounts pa on pa.id = pm.account_id
		where pa.kind = 'personal' and pm.subject_id = agm.added_by_subject_id
		  and not exists (
			select 1 from agent_registrations r
			where r.account_id = pa.id and r.subject_id <> pm.subject_id
		  )
		order by pm.created_at
		limit 1
	) ab on true`

func (s *AuthStore) CreateGroup(ctx context.Context, accountSlug, name, description, createdBySubjectID string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	createdBySubjectID = strings.TrimSpace(createdBySubjectID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var accountID string
	if err := tx.QueryRowContext(ctx, `select id from accounts where slug = $1`, accountSlug).Scan(&accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	groupID, err := objectid.RandomID("grp")
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into account_groups(id, account_id, name, description, created_by_subject_id, created_at, updated_at)
		values ($1, $2, $3, $4, nullif($5, ''), now(), now())
	`, groupID, accountID, name, description, createdBySubjectID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: group %s already exists", ErrConflict, storage.FormatGroupHandle(accountSlug, name))
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetGroup(ctx, accountSlug, name)
}

func (s *AuthStore) GetGroup(ctx context.Context, accountSlug, name string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	var g storage.AccountGroup
	err = s.db.QueryRowContext(ctx, `
		select g.id, a.slug, g.name, g.description, coalesce(cb.slug, g.created_by_subject_id, ''), g.created_at, g.updated_at
		from account_groups g
		join accounts a on a.id = g.account_id
		left join lateral (
			select pa.slug
			from account_memberships pm
			join accounts pa on pa.id = pm.account_id
			where pa.kind = 'personal' and pm.subject_id = g.created_by_subject_id
			  and not exists (
				select 1 from agent_registrations r
				where r.account_id = pa.id and r.subject_id <> pm.subject_id
			  )
			order by pm.created_at
			limit 1
		) cb on true
		where a.slug = $1 and g.name = $2
	`, accountSlug, name).Scan(&g.ID, &g.Account, &g.Name, &g.Description, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.Handle = storage.FormatGroupHandle(g.Account, g.Name)
	members, err := s.listGroupMembersByID(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	g.Members = members
	g.MemberCount = int32(len(members))
	return &g, nil
}

func (s *AuthStore) ListGroups(ctx context.Context, accountSlug string) ([]storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `select exists(select 1 from accounts where slug = $1)`, accountSlug).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		select g.id, a.slug, g.name, g.description, coalesce(cb.slug, g.created_by_subject_id, ''), g.created_at, g.updated_at,
		       (select count(*) from account_group_members agm where agm.group_id = g.id)
		from account_groups g
		join accounts a on a.id = g.account_id
		left join lateral (
			select pa.slug
			from account_memberships pm
			join accounts pa on pa.id = pm.account_id
			where pa.kind = 'personal' and pm.subject_id = g.created_by_subject_id
			  and not exists (
				select 1 from agent_registrations r
				where r.account_id = pa.id and r.subject_id <> pm.subject_id
			  )
			order by pm.created_at
			limit 1
		) cb on true
		where a.slug = $1
		order by g.name
	`, accountSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.AccountGroup
	for rows.Next() {
		var g storage.AccountGroup
		if err := rows.Scan(&g.ID, &g.Account, &g.Name, &g.Description, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		g.Handle = storage.FormatGroupHandle(g.Account, g.Name)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		members, err := s.listGroupMembersByID(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Members = members
		out[i].MemberCount = int32(len(members))
	}
	return out, nil
}

func (s *AuthStore) DeleteGroup(ctx context.Context, accountSlug, name string) error {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		delete from account_groups g
		using accounts a
		where g.account_id = a.id and a.slug = $1 and g.name = $2
	`, accountSlug, name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *AuthStore) AddGroupMember(ctx context.Context, accountSlug, name, subjectID, addedBySubjectID string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	addedBySubjectID = strings.TrimSpace(addedBySubjectID)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var groupID string
	err = tx.QueryRowContext(ctx, `
		select g.id
		from account_groups g
		join accounts a on a.id = g.account_id
		where a.slug = $1 and g.name = $2
	`, accountSlug, name).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var subjectExists bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from subjects where id = $1)`, subjectID).Scan(&subjectExists); err != nil {
		return nil, err
	}
	if !subjectExists {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		insert into account_group_members(group_id, subject_id, added_by_subject_id, created_at)
		values ($1, $2, nullif($3, ''), now())
		on conflict (group_id, subject_id) do nothing
	`, groupID, subjectID, addedBySubjectID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		update account_groups set updated_at = $2 where id = $1
	`, groupID, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetGroup(ctx, accountSlug, name)
}

func (s *AuthStore) RemoveGroupMember(ctx context.Context, accountSlug, name, subjectID string) (*storage.AccountGroup, error) {
	accountSlug, err := storage.NormalizeGroupAccount(accountSlug)
	if err != nil {
		return nil, err
	}
	name, err = storage.NormalizeGroupName(name)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var groupID string
	err = tx.QueryRowContext(ctx, `
		select g.id
		from account_groups g
		join accounts a on a.id = g.account_id
		where a.slug = $1 and g.name = $2
	`, accountSlug, name).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		delete from account_group_members where group_id = $1 and subject_id = $2
	`, groupID, subjectID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		update account_groups set updated_at = $2 where id = $1
	`, groupID, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetGroup(ctx, accountSlug, name)
}

func (s *AuthStore) SliceGroupRole(ctx context.Context, subjectID, sliceID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `
		select sgm.role
		from slice_group_members sgm
		join account_group_members agm on agm.group_id = sgm.group_id
		where sgm.slice_id = $1 and agm.subject_id = $2
		order by case sgm.role
			when 'admin' then 1
			when 'writer' then 2
			when 'contributor' then 3
			when 'reader' then 4
			else 5
		end
		limit 1
	`, strings.TrimSpace(sliceID), strings.TrimSpace(subjectID)).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

func (s *AuthStore) listGroupMembersByID(ctx context.Context, groupID string) ([]storage.AccountGroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		select agm.subject_id, coalesce(mp.slug, ''), coalesce(mp.display_name, ''), coalesce(ms.kind, ''),
		       coalesce(ab.slug, agm.added_by_subject_id, ''), agm.created_at
		from account_group_members agm`+groupSubjectPersonalJoinSQL+`
		where agm.group_id = $1
		order by agm.created_at, agm.subject_id
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.AccountGroupMember
	for rows.Next() {
		var m storage.AccountGroupMember
		if err := rows.Scan(&m.SubjectID, &m.Username, &m.DisplayName, &m.Kind, &m.AddedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func groupMemberSubjectIDsTx(ctx context.Context, q publishQueryer, groupRef string) (map[string]bool, error) {
	accountSlug, name, ok := storage.ParseGroupRef(groupRef)
	if !ok {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `
		select agm.subject_id
		from account_group_members agm
		join account_groups g on g.id = agm.group_id
		join accounts a on a.id = g.account_id
		where a.slug = $1 and g.name = $2
	`, accountSlug, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
