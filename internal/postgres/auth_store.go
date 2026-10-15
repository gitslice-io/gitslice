package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
)

type AuthStore struct {
	db    *sql.DB
	trees *treestore.Store
}

func (s *AuthStore) subjectForAPIKey(ctx context.Context, token string) (*Subject, error) {
	var subject Subject
	var keyID string
	var lastUsedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		select api_keys.id, api_keys.last_used_at, subjects.id, subjects.display_name
		from api_keys
		join subjects on subjects.id = api_keys.subject_id
		where api_keys.token_hash = $1
		  and api_keys.revoked_at is null
		  and (api_keys.expires_at is null or api_keys.expires_at > now())
	`, tokenHash(token)).Scan(&keyID, &lastUsedAt, &subject.ID, &subject.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if !lastUsedAt.Valid || time.Since(lastUsedAt.Time) >= apiKeyLastUsedGranularity {
		// Best effort: a failed usage stamp must not fail authentication.
		_, _ = s.db.ExecContext(ctx, `
			update api_keys
			set last_used_at = now()
			where id = $1
			  and (last_used_at is null or last_used_at < now() - $2::interval)
		`, keyID, fmt.Sprintf("%d seconds", int(apiKeyLastUsedGranularity/time.Second)))
	}
	return &subject, nil
}

func (s *AuthStore) SubjectForToken(ctx context.Context, token string) (*Subject, error) {
	if strings.HasPrefix(token, storage.APIKeyPrefix) {
		return s.subjectForAPIKey(ctx, token)
	}
	var subject Subject
	err := s.db.QueryRowContext(ctx, `
		select subjects.id, subjects.display_name
		from sessions
		join subjects on subjects.id = sessions.subject_id
		where sessions.token_hash = $1
		  and sessions.revoked_at is null
		  and sessions.expires_at > now()
	`, tokenHash(token)).Scan(&subject.ID, &subject.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	return &subject, nil
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
			when 'reader' then 5
			when 'guest' then 6
			else 7
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
