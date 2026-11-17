package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
)

type AuthStore struct {
	db    *sql.DB
	trees *treestore.Store
}

func (s *AuthStore) principalForAPIKey(ctx context.Context, token string) (*authctx.Principal, error) {
	var principal authctx.Principal
	var lastUsedAt sql.NullTime
	var sliceIDsJSON string
	err := s.db.QueryRowContext(ctx, `
		with recursive chain as (
			select k.id, k.subject_id, k.last_used_at, k.slice_ids, k.max_role,
			       k.parent_key_id, k.purpose, k.daemon_id, k.conversation_id,
			       0 as depth,
			       array[k.id] as visited
			from api_keys k
			where k.token_hash = $1
			  and k.revoked_at is null
			  and (k.expires_at is null or k.expires_at > now())
			union all
			select p.id, p.subject_id, p.last_used_at, p.slice_ids, p.max_role,
			       p.parent_key_id, p.purpose, p.daemon_id, p.conversation_id,
			       c.depth + 1,
			       c.visited || p.id
			from api_keys p
			join chain c on p.id = c.parent_key_id
			where p.revoked_at is null
			  and (p.expires_at is null or p.expires_at > now())
			  and not (p.id = any(c.visited))
		)
		select leaf.id, leaf.last_used_at,
		       leaf.slice_ids is not null,
		       coalesce(to_json(leaf.slice_ids)::text, '[]'),
		       coalesce(leaf.max_role, ''),
		       leaf.purpose, leaf.daemon_id, leaf.conversation_id,
		       subjects.id, subjects.kind, coalesce(subjects.controller_subject_id, '')
		from chain leaf
		join subjects on subjects.id = leaf.subject_id
		where leaf.depth = 0
		  and exists (
			select 1 from chain root
			where root.parent_key_id is null
		  )
	`, tokenHash(token)).Scan(
		&principal.KeyID, &lastUsedAt,
		&principal.HasSliceScope, &sliceIDsJSON, &principal.MaxRole,
		&principal.Purpose, &principal.DaemonID, &principal.ConversationID,
		&principal.SubjectID, &principal.SubjectKind, &principal.ControllerSubjectID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if principal.HasSliceScope {
		if err := json.Unmarshal([]byte(sliceIDsJSON), &principal.SliceIDs); err != nil {
			return nil, err
		}
		if principal.SliceIDs == nil {
			principal.SliceIDs = []string{}
		}
	}
	if !lastUsedAt.Valid || time.Since(lastUsedAt.Time) >= apiKeyLastUsedGranularity {
		// Best effort: a failed usage stamp must not fail authentication.
		_, _ = s.db.ExecContext(ctx, `
			update api_keys
			set last_used_at = now()
			where id = $1
			  and (last_used_at is null or last_used_at < now() - $2::interval)
		`, principal.KeyID, fmt.Sprintf("%d seconds", int(apiKeyLastUsedGranularity/time.Second)))
	}
	return &principal, nil
}

func (s *AuthStore) AuthenticateToken(ctx context.Context, token string) (*authctx.Principal, error) {
	if strings.HasPrefix(token, storage.APIKeyPrefix) {
		return s.principalForAPIKey(ctx, token)
	}
	var principal authctx.Principal
	err := s.db.QueryRowContext(ctx, `
		select subjects.id, subjects.kind, coalesce(subjects.controller_subject_id, '')
		from sessions
		join subjects on subjects.id = sessions.subject_id
		where sessions.token_hash = $1
		  and sessions.revoked_at is null
		  and sessions.expires_at > now()
	`, tokenHash(token)).Scan(&principal.SubjectID, &principal.SubjectKind, &principal.ControllerSubjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	return &principal, nil
}

func (s *AuthStore) SubjectForToken(ctx context.Context, token string) (*Subject, error) {
	principal, err := s.AuthenticateToken(ctx, token)
	if err != nil {
		return nil, err
	}
	subjects, err := s.SubjectsByIDs(ctx, []string{principal.SubjectID})
	if err != nil {
		return nil, err
	}
	if subj, ok := subjects[principal.SubjectID]; ok {
		return &subj, nil
	}
	return &Subject{
		ID:                  principal.SubjectID,
		Kind:                principal.SubjectKind,
		ControllerSubjectID: principal.ControllerSubjectID,
	}, nil
}

func (s *AuthStore) SubjectsByIDs(ctx context.Context, subjectIDs []string) (map[string]storage.Subject, error) {
	return querySubjectsByIDs(ctx, s.db, subjectIDs)
}

func (s *AuthStore) EnsureAccountMember(ctx context.Context, subjectID, accountSlug string) error {
	_, err := s.AccountRole(ctx, subjectID, accountSlug)
	return err
}

func (s *AuthStore) AccountRole(ctx context.Context, subjectID, accountSlug string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `
		select m.role
		from account_memberships m
		join accounts a on a.id = m.account_id
		where a.slug = $1 and m.subject_id = $2
		order by case m.role
			when 'owner' then 1
			when 'admin' then 2
			when 'writer' then 3
			when 'member' then 4
			when 'contributor' then 5
			when 'reader' then 6
			when 'guest' then 7
			else 8
		end
		limit 1
	`, accountSlug, subjectID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

func (s *AuthStore) ListSubjectAccountSlugs(ctx context.Context, subjectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		select a.slug
		from account_memberships m
		join accounts a on a.id = m.account_id
		where m.subject_id = $1
		order by
			-- The caller's own personal account comes first, whether the
			-- subject is a seeded user, an external login or an agent;
			-- callers such as daemon registration key off it. Agent accounts
			-- the caller claimed come last.
			case
				when a.kind = 'personal' and `+notOthersAgentAccountSQL+` then 0
				when not (`+notOthersAgentAccountSQL+`) then 2
				else 1
			end,
			a.slug
	`, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

func (s *AuthStore) ExternalIdentity(ctx context.Context, subjectID string) (string, string, error) {
	var provider, externalID string
	err := s.db.QueryRowContext(ctx, `
		select coalesce(external_provider, ''), coalesce(external_subject, '')
		from subjects
		where id = $1
	`, strings.TrimSpace(subjectID)).Scan(&provider, &externalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return provider, externalID, nil
}

func (s *AuthStore) VerifiedEmails(ctx context.Context, subjectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		select email from subject_emails where subject_id = $1 order by email
	`, strings.TrimSpace(subjectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}

func (s *AuthStore) SetVerifiedEmails(ctx context.Context, subjectID, source string, emails []string) error {
	subjectID = strings.TrimSpace(subjectID)
	source = strings.TrimSpace(source)
	if subjectID == "" || source == "" {
		return fmt.Errorf("%w: subject and source are required", ErrInvalid)
	}
	normalized := storage.NormalizeEmails(emails)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		delete from subject_emails
		where subject_id = $1 and source = $2 and not (email = any($3))
	`, subjectID, source, normalized); err != nil {
		return err
	}
	for _, email := range normalized {
		if _, err := tx.ExecContext(ctx, `
			insert into subject_emails(subject_id, email, source, verified_at)
			values ($1, $2, $3, now())
			on conflict (subject_id, email) do update
			set source = excluded.source, verified_at = excluded.verified_at
		`, subjectID, email, source); err != nil {
			return err
		}
	}
	return tx.Commit()
}
