package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

// TestAgentAPIKeysAreManagedByTheAgentAndItsOwners follows a lost key: the
// agent's owner lists its keys, creates a replacement, and revokes the lost one,
// while strangers and unclaimed humans can do none of it.
func TestAgentAPIKeysAreManagedByTheAgentAndItsOwners(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	auth := store.Auth()
	agent, err := auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "keybot", OwnerEmail: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "otherbot", OwnerEmail: "someone@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_stranger", "")
	if err != nil {
		t.Fatal(err)
	}

	// Before the claim the owner has no say; strangers never do. Both get
	// "not found", so the call does not say which accounts are agents.
	for _, actor := range []string{owner, stranger} {
		if _, _, _, err := auth.ListAPIKeys(ctx, actor, "keybot"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ListAPIKeys by %s before the claim: err = %v, want ErrNotFound", actor, err)
		}
	}
	if err := auth.SetVerifiedEmails(ctx, owner, storage.ProviderClerk, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AcceptClaim(ctx, owner, agent.SubjectID); err != nil {
		t.Fatal(err)
	}

	subjectID, account, keys, err := auth.ListAPIKeys(ctx, owner, "keybot")
	if err != nil || subjectID != agent.SubjectID || account != "keybot" || len(keys) != 1 || keys[0].Name != "default" || !keys[0].Active(time.Now()) {
		t.Fatalf("owner's ListAPIKeys = %q %q %+v, %v", subjectID, account, keys, err)
	}
	lostKeyID := keys[0].ID

	// The owner replaces the lost key.
	created, secret, createdFor, _, err := auth.CreateAPIKey(ctx, owner, "KeyBot", "replacement")
	if err != nil || createdFor != agent.SubjectID || created.Name != "replacement" || !strings.HasPrefix(secret, storage.APIKeyPrefix) {
		t.Fatalf("CreateAPIKey = %+v %q for %q, %v", created, secret, createdFor, err)
	}
	if got, err := auth.SubjectForToken(ctx, secret); err != nil || got.ID != agent.SubjectID {
		t.Fatalf("the new key should sign in as the agent: %+v, %v", got, err)
	}
	if _, err := auth.SubjectForToken(ctx, agent.APIKey); err != nil {
		t.Fatalf("the old key still works until it is revoked: %v", err)
	}

	// Revoking the lost key ends it at once, and is safe to repeat.
	revoked, err := auth.RevokeAPIKey(ctx, owner, "keybot", lostKeyID)
	if err != nil || revoked.RevokedAt.IsZero() {
		t.Fatalf("RevokeAPIKey = %+v, %v", revoked, err)
	}
	if _, err := auth.SubjectForToken(ctx, agent.APIKey); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a revoked key must not sign in: err = %v", err)
	}
	if again, err := auth.RevokeAPIKey(ctx, owner, "keybot", lostKeyID); err != nil || !again.RevokedAt.Equal(revoked.RevokedAt) {
		t.Fatalf("revoking twice = %+v, %v; want the first revocation time kept", again, err)
	}
	if _, err := auth.SubjectForToken(ctx, secret); err != nil {
		t.Fatalf("the new key keeps working: %v", err)
	}

	// The agent can manage its own keys without naming itself.
	if _, _, _, _, err := auth.CreateAPIKey(ctx, agent.SubjectID, "", ""); err != nil {
		t.Fatalf("an agent creating its own key: %v", err)
	}
	if _, _, listed, err := auth.ListAPIKeys(ctx, agent.SubjectID, ""); err != nil || len(listed) != 3 {
		t.Fatalf("agent's own ListAPIKeys = %d keys, %v; want 3", len(listed), err)
	}
	if _, _, listed, _ := auth.ListAPIKeys(ctx, owner, "keybot"); listed[0].Name != "key" {
		t.Fatalf("an empty name should become \"key\", newest first: %+v", listed)
	}

	// Nobody else: not the stranger, not another agent, not a key of another agent.
	if _, _, _, _, err := auth.CreateAPIKey(ctx, stranger, "keybot", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger creating a key: err = %v, want ErrNotFound", err)
	}
	if _, _, _, err := auth.ListAPIKeys(ctx, other.SubjectID, "keybot"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another agent listing keys: err = %v, want ErrNotFound", err)
	}
	if _, _, _, err := auth.ListAPIKeys(ctx, owner, "payment"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an account that is not an agent: err = %v, want ErrNotFound", err)
	}
	_, _, otherKeys, _ := auth.ListAPIKeys(ctx, other.SubjectID, "")
	if _, err := auth.RevokeAPIKey(ctx, owner, "keybot", otherKeys[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking another agent's key by id: err = %v, want ErrNotFound", err)
	}
	if _, err := auth.SubjectForToken(ctx, other.APIKey); err != nil {
		t.Fatalf("the other agent's key must be untouched: %v", err)
	}

	// Names are checked, and an agent holds a limited number of active keys.
	if _, _, _, _, err := auth.CreateAPIKey(ctx, owner, "keybot", strings.Repeat("x", storage.MaxAPIKeyNameLength+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a long name: err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := auth.CreateAPIKey(ctx, owner, "keybot", "bad\x00name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a control character in the name: err = %v, want ErrInvalid", err)
	}
	for {
		_, _, _, _, err := auth.CreateAPIKey(ctx, owner, "keybot", "filler")
		if errors.Is(err, ErrConflict) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, _, listed, _ := auth.ListAPIKeys(ctx, owner, "keybot"); countActive(listed) != storage.MaxActiveAPIKeys {
		t.Fatalf("active keys at the limit = %d, want %d", countActive(listed), storage.MaxActiveAPIKeys)
	}
}

func countActive(keys []storage.APIKey) int {
	n := 0
	for _, key := range keys {
		if key.Active(time.Now()) {
			n++
		}
	}
	return n
}
