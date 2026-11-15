package postgres

import (
	"database/sql"
	"testing"
	"time"
)

func TestAuthStoreCLILoginDeviceFlow(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	// Seed an expired CLI login row and verify StartCliLogin prunes it.
	if _, err := store.db.ExecContext(ctx, `
		insert into cli_login_sessions(code_hash, status, expires_at, created_at)
		values ('expired_code_seed_hash', 'pending', now() - interval '5 minutes', now() - interval '15 minutes')
	`); err != nil {
		t.Fatal(err)
	}

	code, expiresAt, err := store.Auth().StartCliLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" || !expiresAt.After(time.Now().UTC()) {
		t.Fatalf("StartCliLogin = (%q, %s), want code and future expiry", code, expiresAt)
	}

	var expiredCount int
	if err := store.db.QueryRowContext(ctx, `
		select count(*) from cli_login_sessions where code_hash = 'expired_code_seed_hash'
	`).Scan(&expiredCount); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 0 {
		t.Fatalf("expired cli_login_sessions count = %d, want 0 after StartCliLogin prune", expiredCount)
	}

	status, token, subjectID, err := store.Auth().PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if status != "pending" || token != "" || subjectID != "" {
		t.Fatalf("pending PollCliLogin = (%q, %q, %q), want pending without token", status, token, subjectID)
	}

	if err := store.Auth().CompleteCliLogin(ctx, code, "user_alice"); err != nil {
		t.Fatal(err)
	}

	// CompleteCliLogin must NOT persist a raw bearer session_token in cli_login_sessions.
	var storedRawToken sql.NullString
	if err := store.db.QueryRowContext(ctx, `
		select session_token from cli_login_sessions where code_hash = $1
	`, tokenHash(code)).Scan(&storedRawToken); err != nil {
		t.Fatal(err)
	}
	if storedRawToken.Valid && storedRawToken.String != "" {
		t.Fatalf("cli_login_sessions.session_token = %q, want NULL before PollCliLogin", storedRawToken.String)
	}

	status, token, subjectID, err = store.Auth().PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if status != "approved" || token == "" || subjectID != "user_alice" {
		t.Fatalf("approved PollCliLogin = (%q, %q, %q), want approved token for user_alice", status, token, subjectID)
	}
	subject, err := store.Auth().SubjectForToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if subject.ID != "user_alice" {
		t.Fatalf("SubjectForToken = %q, want user_alice", subject.ID)
	}

	var sessionExpiresAt time.Time
	if err := store.db.QueryRowContext(ctx, `
		select expires_at
		from sessions
		where token_hash = $1
	`, tokenHash(token)).Scan(&sessionExpiresAt); err != nil {
		t.Fatal(err)
	}
	sessionTTL := time.Until(sessionExpiresAt)
	if sessionTTL < 29*24*time.Hour || sessionTTL > 31*24*time.Hour {
		t.Fatalf("CLI session TTL = %s, want about 30 days", sessionTTL)
	}

	status, token, subjectID, err = store.Auth().PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if status != "expired" || token != "" || subjectID != "" {
		t.Fatalf("second PollCliLogin = (%q, %q, %q), want expired without token", status, token, subjectID)
	}
}
