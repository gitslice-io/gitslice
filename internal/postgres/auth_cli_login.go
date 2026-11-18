package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
)

const (
	cliLoginTTL            = 10 * time.Minute
	cliLoginSessionTTL     = 30 * 24 * time.Hour
	cliLoginTokenPrefix    = "clitok"
	cliLoginStatusPending  = "pending"
	cliLoginStatusApproved = "approved"
	cliLoginStatusExpired  = "expired"

	// apiKeyLastUsedGranularity bounds how often SubjectForToken rewrites
	// api_keys.last_used_at, so key auth is not a write on every request.
	apiKeyLastUsedGranularity = time.Minute
)

func (s *AuthStore) StartCliLogin(ctx context.Context) (string, time.Time, error) {
	code, err := objectid.RandomID("cli")
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(cliLoginTTL)
	if _, err := s.db.ExecContext(ctx, `delete from cli_login_sessions where expires_at <= now()`); err != nil {
		return "", time.Time{}, err
	}
	if _, err := s.db.ExecContext(ctx, `
		insert into cli_login_sessions(code_hash, status, expires_at)
		values ($1, $2, $3)
	`, tokenHash(code), cliLoginStatusPending, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return code, expiresAt, nil
}

func (s *AuthStore) CompleteCliLogin(ctx context.Context, code, subjectID string) error {
	code = strings.TrimSpace(code)
	subjectID = strings.TrimSpace(subjectID)
	if code == "" || subjectID == "" {
		return ErrNotFound
	}

	return execInTx(ctx, s.db, func(tx *sql.Tx) error {
		var existingSubject string
		err := tx.QueryRowContext(ctx, `select id from subjects where id = $1`, subjectID).Scan(&existingSubject)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		var codeHash string
		err = tx.QueryRowContext(ctx, `
			select code_hash
			from cli_login_sessions
			where code_hash = $1
			  and status = $2
			  and expires_at > now()
			for update
		`, tokenHash(code), cliLoginStatusPending).Scan(&codeHash)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		// Do not mint or persist a raw session token in cli_login_sessions; mint
		// the session lazily when the CLI polls with the raw login code.
		_, err = tx.ExecContext(ctx, `
			update cli_login_sessions
			set status = $2,
			    subject_id = $3,
			    session_token = null
			where code_hash = $1
		`, codeHash, cliLoginStatusApproved, subjectID)
		return err
	})
}

func (s *AuthStore) PollCliLogin(ctx context.Context, code string) (string, string, string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return cliLoginStatusExpired, "", "", nil
	}

	type pollResult struct {
		status    string
		token     string
		subjectID string
	}
	res, err := inTx(ctx, s.db, func(tx *sql.Tx) (pollResult, error) {
		var status, legacyToken, subjectID string
		var expiresAt time.Time
		codeHash := tokenHash(code)
		err := tx.QueryRowContext(ctx, `
			select status, coalesce(session_token, ''), coalesce(subject_id, ''), expires_at
			from cli_login_sessions
			where code_hash = $1
			for update
		`, codeHash).Scan(&status, &legacyToken, &subjectID, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return pollResult{status: cliLoginStatusExpired}, nil
		}
		if err != nil {
			return pollResult{}, err
		}
		if !expiresAt.After(time.Now().UTC()) {
			if _, err := tx.ExecContext(ctx, `delete from cli_login_sessions where expires_at <= now() or code_hash = $1`, codeHash); err != nil {
				return pollResult{}, err
			}
			return pollResult{status: cliLoginStatusExpired}, nil
		}
		if status == cliLoginStatusPending {
			return pollResult{status: cliLoginStatusPending}, nil
		}
		if status == cliLoginStatusApproved {
			if _, err := tx.ExecContext(ctx, `delete from cli_login_sessions where code_hash = $1`, codeHash); err != nil {
				return pollResult{}, err
			}
			token := legacyToken
			if token == "" {
				var sessionID, hashedToken string
				var sessionExpiresAt time.Time
				token, sessionID, hashedToken, sessionExpiresAt, err = newSessionWithTTL(subjectID, cliLoginTokenPrefix, cliLoginSessionTTL)
				if err != nil {
					return pollResult{}, err
				}
				if _, err := tx.ExecContext(ctx, `
					insert into sessions(id, subject_id, token_hash, expires_at)
					values ($1, $2, $3, $4)
				`, sessionID, subjectID, hashedToken, sessionExpiresAt); err != nil {
					return pollResult{}, err
				}
			}
			return pollResult{status: cliLoginStatusApproved, token: token, subjectID: subjectID}, nil
		}
		return pollResult{status: cliLoginStatusExpired}, nil
	})
	if err != nil {
		return "", "", "", err
	}
	return res.status, res.token, res.subjectID, nil
}

func newSessionWithTTL(subjectID, tokenPrefix string, ttl time.Duration) (token, sessionID, hashedToken string, expiresAt time.Time, err error) {
	token, err = objectid.RandomID(tokenPrefix)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	sessionID, err = objectid.RandomID("sess")
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	return token, sessionID, tokenHash(token), time.Now().UTC().Add(ttl), nil
}
