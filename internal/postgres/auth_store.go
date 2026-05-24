package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
)

type AuthStore struct {
	db *sql.DB
}

func (s *AuthStore) LoginDevUser(ctx context.Context, devUser string) (string, string, error) {
	subjectID := normalizeDevSubject(devUser)
	var found string
	err := s.db.QueryRowContext(ctx, `select id from subjects where id = $1`, subjectID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	token, err := objectid.RandomID("devtok")
	if err != nil {
		return "", "", err
	}
	sessionID, err := objectid.RandomID("sess")
	if err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `
		insert into sessions(id, subject_id, token_hash, expires_at)
		values ($1, $2, $3, $4)
	`, sessionID, subjectID, tokenHash(token), time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		return "", "", err
	}
	return token, subjectID, nil
}

func (s *AuthStore) SubjectForToken(ctx context.Context, token string) (*Subject, error) {
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
	var ok bool
	err := s.db.QueryRowContext(ctx, `
		select exists(
			select 1
			from account_memberships m
			join accounts a on a.id = m.account_id
			where a.slug = $1 and m.subject_id = $2
		)
	`, accountSlug, subjectID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnauthorized
	}
	return nil
}
