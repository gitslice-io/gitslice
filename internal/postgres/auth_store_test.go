package postgres

import (
	"testing"
	"time"
)

func TestAuthStoreCLILoginDeviceFlow(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	code, expiresAt, err := store.Auth().StartCliLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" || !expiresAt.After(time.Now().UTC()) {
		t.Fatalf("StartCliLogin = (%q, %s), want code and future expiry", code, expiresAt)
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
