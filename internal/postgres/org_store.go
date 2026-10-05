package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/storage"
)

func (s *AuthStore) SubjectIDForUsername(ctx context.Context, username string) (string, error) {
	var subjectID string
	err := s.db.QueryRowContext(ctx, `
		select m.subject_id
		from account_memberships m
		join accounts a on a.id = m.account_id
		where a.kind = 'personal' and a.slug = $1
		  and `+notOthersAgentAccountSQL+`
		order by m.created_at, m.subject_id
		limit 1
	`, strings.TrimSpace(username)).Scan(&subjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return subjectID, err
}

func (s *AuthStore) AccountKind(ctx context.Context, accountSlug string) (string, error) {
	var kind string
	err := s.db.QueryRowContext(ctx, `select kind from accounts where slug = $1`, strings.TrimSpace(accountSlug)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return kind, err
}

func (s *AuthStore) CreateOrganization(ctx context.Context, slug string, ownerSubjectIDs []string, createdBy string) (err error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || len(ownerSubjectIDs) == 0 {
		return fmt.Errorf("%w: organization slug and owners are required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	accountID := signupAccountID(slug)
	res, err := tx.ExecContext(ctx, `
		insert into accounts(id, slug, kind, created_by_subject_id, created_at, updated_at)
		values ($1, $2, $3, nullif($4, ''), now(), now())
		on conflict do nothing
	`, accountID, slug, storage.AccountKindOrganization, strings.TrimSpace(createdBy))
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return fmt.Errorf("%w: account %q already exists", ErrConflict, slug)
	}
	for _, owner := range ownerSubjectIDs {
		if _, err = tx.ExecContext(ctx, `
			insert into account_memberships(account_id, subject_id, role, created_at)
			values ($1, $2, 'owner', now())
			on conflict do nothing
		`, accountID, owner); err != nil {
			return err
		}
	}
	if err = s.provisionHomeTx(ctx, tx, accountID, slug, createdBy); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AuthStore) ListAccountMembers(ctx context.Context, accountSlug string) ([]storage.AccountMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		select m.subject_id, m.role,
		       coalesce((
				select pa.slug
				from account_memberships pm
				join accounts pa on pa.id = pm.account_id
				where pa.kind = 'personal' and pm.subject_id = m.subject_id
				  and not exists (
					select 1 from agent_registrations r
					where r.account_id = pa.id and r.subject_id <> pm.subject_id
				  )
				order by pm.created_at
				limit 1
		       ), '')
		from account_memberships m
		join accounts a on a.id = m.account_id
		where a.slug = $1
		order by m.created_at, m.subject_id
	`, strings.TrimSpace(accountSlug))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySubject := map[string]*storage.AccountMember{}
	var order []string
	for rows.Next() {
		var member storage.AccountMember
		if err := rows.Scan(&member.SubjectID, &member.Role, &member.Username); err != nil {
			return nil, err
		}
		existing, ok := bySubject[member.SubjectID]
		if !ok {
			copied := member
			bySubject[member.SubjectID] = &copied
			order = append(order, member.SubjectID)
			continue
		}
		if storage.AccountRoleRank(member.Role) < storage.AccountRoleRank(existing.Role) {
			existing.Role = member.Role
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members := make([]storage.AccountMember, 0, len(order))
	for _, subjectID := range order {
		members = append(members, *bySubject[subjectID])
	}
	sortAccountMembers(members)
	return members, nil
}

func (s *AuthStore) SetAccountMemberRole(ctx context.Context, accountSlug, subjectID, role string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	accountID, err := organizationAccountForUpdateTx(ctx, tx, accountSlug)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `delete from account_memberships where account_id = $1 and subject_id = $2`, accountID, subjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values ($1, $2, $3, now())
	`, accountID, subjectID, role); err != nil {
		return err
	}
	if err = requireAnOwnerTx(ctx, tx, accountID, accountSlug); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AuthStore) RemoveAccountMember(ctx context.Context, accountSlug, subjectID string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	accountID, err := organizationAccountForUpdateTx(ctx, tx, accountSlug)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `delete from account_memberships where account_id = $1 and subject_id = $2`, accountID, subjectID)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return fmt.Errorf("%w: subject is not a member of %q", ErrNotFound, accountSlug)
	}
	if err = requireAnOwnerTx(ctx, tx, accountID, accountSlug); err != nil {
		return err
	}
	return tx.Commit()
}

// organizationAccountForUpdateTx locks an organization account row, so that
// concurrent membership changes cannot both remove the last owner.
func organizationAccountForUpdateTx(ctx context.Context, tx *sql.Tx, accountSlug string) (string, error) {
	var accountID, kind string
	err := tx.QueryRowContext(ctx, `
		select id, kind from accounts where slug = $1 for update
	`, strings.TrimSpace(accountSlug)).Scan(&accountID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if kind != storage.AccountKindOrganization {
		return "", fmt.Errorf("%w: members can only be managed on organization accounts", ErrConflict)
	}
	return accountID, nil
}

func requireAnOwnerTx(ctx context.Context, tx *sql.Tx, accountID, accountSlug string) error {
	var owners int
	if err := tx.QueryRowContext(ctx, `
		select count(*) from account_memberships where account_id = $1 and role = 'owner'
	`, accountID).Scan(&owners); err != nil {
		return err
	}
	if owners == 0 {
		return fmt.Errorf("%w: %q must keep at least one owner", ErrConflict, accountSlug)
	}
	return nil
}

// sortAccountMembers orders members by role (owners first), then username.
func sortAccountMembers(members []storage.AccountMember) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if ra, rb := storage.AccountRoleRank(a.Role), storage.AccountRoleRank(b.Role); ra != rb {
			return ra < rb
		}
		if a.Username != b.Username {
			return a.Username < b.Username
		}
		return a.SubjectID < b.SubjectID
	})
}

func (s *AuthStore) CountOrganizationsCreatedBy(ctx context.Context, subjectID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		select count(*) from accounts where kind = 'organization' and created_by_subject_id = $1
	`, strings.TrimSpace(subjectID)).Scan(&count)
	return count, err
}

func (s *AuthStore) UpsertAccountInvitation(ctx context.Context, accountSlug, subjectID, role, invitedBy string) (*storage.AccountInvitation, error) {
	var accountID, kind string
	err := s.db.QueryRowContext(ctx, `select id, kind from accounts where slug = $1`, strings.TrimSpace(accountSlug)).Scan(&accountID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if kind != storage.AccountKindOrganization {
		return nil, fmt.Errorf("%w: only organizations take invitations", ErrConflict)
	}
	invitation := storage.AccountInvitation{Account: strings.TrimSpace(accountSlug), SubjectID: subjectID, Role: role, InvitedBySubjectID: invitedBy}
	err = s.db.QueryRowContext(ctx, `
		insert into account_invitations(account_id, subject_id, role, invited_by_subject_id, created_at)
		values ($1, $2, $3, $4, now())
		on conflict (account_id, subject_id) do update
		set role = excluded.role, invited_by_subject_id = excluded.invited_by_subject_id, created_at = excluded.created_at
		returning created_at
	`, accountID, subjectID, role, invitedBy).Scan(&invitation.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &invitation, nil
}

const invitationColumns = `a.slug, i.subject_id, i.role, i.invited_by_subject_id, i.created_at`

func scanInvitations(rows *sql.Rows) ([]storage.AccountInvitation, error) {
	defer rows.Close()
	var out []storage.AccountInvitation
	for rows.Next() {
		var invitation storage.AccountInvitation
		if err := rows.Scan(&invitation.Account, &invitation.SubjectID, &invitation.Role, &invitation.InvitedBySubjectID, &invitation.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, invitation)
	}
	return out, rows.Err()
}

func (s *AuthStore) ListAccountInvitations(ctx context.Context, accountSlug string) ([]storage.AccountInvitation, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+invitationColumns+`
		from account_invitations i join accounts a on a.id = i.account_id
		where a.slug = $1
		order by i.created_at desc, i.subject_id
	`, strings.TrimSpace(accountSlug))
	if err != nil {
		return nil, err
	}
	return scanInvitations(rows)
}

func (s *AuthStore) ListSubjectInvitations(ctx context.Context, subjectID string) ([]storage.AccountInvitation, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+invitationColumns+`
		from account_invitations i join accounts a on a.id = i.account_id
		where i.subject_id = $1
		order by i.created_at desc, a.slug
	`, strings.TrimSpace(subjectID))
	if err != nil {
		return nil, err
	}
	return scanInvitations(rows)
}

func (s *AuthStore) GetAccountInvitation(ctx context.Context, accountSlug, subjectID string) (*storage.AccountInvitation, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+invitationColumns+`
		from account_invitations i join accounts a on a.id = i.account_id
		where a.slug = $1 and i.subject_id = $2
	`, strings.TrimSpace(accountSlug), strings.TrimSpace(subjectID))
	if err != nil {
		return nil, err
	}
	invitations, err := scanInvitations(rows)
	if err != nil {
		return nil, err
	}
	if len(invitations) == 0 {
		return nil, ErrNotFound
	}
	return &invitations[0], nil
}

func (s *AuthStore) AcceptAccountInvitation(ctx context.Context, accountSlug, subjectID string) (role string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	accountID, err := organizationAccountForUpdateTx(ctx, tx, accountSlug)
	if err != nil {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `
		delete from account_invitations where account_id = $1 and subject_id = $2 returning role
	`, accountID, subjectID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `delete from account_memberships where account_id = $1 and subject_id = $2`, accountID, subjectID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values ($1, $2, $3, now())
	`, accountID, subjectID, role); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return role, nil
}

func (s *AuthStore) DeleteAccountInvitation(ctx context.Context, accountSlug, subjectID string) error {
	res, err := s.db.ExecContext(ctx, `
		delete from account_invitations i using accounts a
		where a.id = i.account_id and a.slug = $1 and i.subject_id = $2
	`, strings.TrimSpace(accountSlug), strings.TrimSpace(subjectID))
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *AuthStore) GetAccountProfile(ctx context.Context, accountSlug string) (*storage.AccountProfile, error) {
	var profile storage.AccountProfile
	err := s.db.QueryRowContext(ctx, `
		select slug, kind, display_name, description, website, created_at from accounts where slug = $1
	`, strings.TrimSpace(accountSlug)).Scan(&profile.Account, &profile.Kind, &profile.DisplayName, &profile.Description, &profile.Website, &profile.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *AuthStore) UpdateAccountProfile(ctx context.Context, accountSlug string, profile storage.AccountProfile) (*storage.AccountProfile, error) {
	res, err := s.db.ExecContext(ctx, `
		update accounts set display_name = $2, description = $3, website = $4, updated_at = now() where slug = $1
	`, strings.TrimSpace(accountSlug), profile.DisplayName, profile.Description, profile.Website)
	if err != nil {
		return nil, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if affected == 0 {
		return nil, ErrNotFound
	}
	return s.GetAccountProfile(ctx, accountSlug)
}
