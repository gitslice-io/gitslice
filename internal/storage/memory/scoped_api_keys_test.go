package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
)

func TestScopedAndChildAPIKeysMemory(t *testing.T) {
	ctx := context.Background()
	stores := New()
	auth := stores.Auth

	userID, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ChooseUsername(ctx, userID, "alice"); err != nil {
		t.Fatal(err)
	}
	agent, err := auth.RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:    "helper-bot",
		OwnerEmail:  "alice@example.com",
		DisplayName: "Helper Bot",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SetVerifiedEmails(ctx, userID, storage.ProviderClerk, []string{"alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AcceptClaim(ctx, userID, agent.SubjectID); err != nil {
		t.Fatal(err)
	}

	// Non-API login session yields empty KeyID.
	code, _, err := auth.StartCliLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CompleteCliLogin(ctx, code, userID); err != nil {
		t.Fatal(err)
	}
	_, sessionToken, _, err := auth.PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	sessPrincipal, err := auth.AuthenticateToken(ctx, sessionToken)
	if err != nil || sessPrincipal.SubjectID != userID || sessPrincipal.KeyID != "" {
		t.Fatalf("session AuthenticateToken = %+v, %v", sessPrincipal, err)
	}

	// User creates a self-key (agent == "") with scope, max_role, expiry, purpose.
	expParent := time.Now().UTC().Add(2 * time.Hour)
	parentKey, parentSecret, targetID, acct, err := auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:       userID,
		Name:          "ci-parent",
		HasSliceScope: true,
		SliceIDs:      []string{"sl_a", "sl_b", "sl_a"},
		MaxRole:       "writer",
		ExpiresAt:     expParent,
		Purpose:       "deploy",
	})
	if err != nil || targetID != userID || acct != "alice" {
		t.Fatalf("user CreateAPIKey = %+v, target=%q acct=%q err=%v", parentKey, targetID, acct, err)
	}
	if len(parentKey.SliceIDs) != 2 || parentKey.SliceIDs[0] != "sl_a" || parentKey.SliceIDs[1] != "sl_b" {
		t.Fatalf("deduped slice_ids = %#v", parentKey.SliceIDs)
	}
	parentPrincipal, err := auth.AuthenticateToken(ctx, parentSecret)
	if err != nil || parentPrincipal.KeyID != parentKey.ID || !parentPrincipal.HasSliceScope || parentPrincipal.MaxRole != "writer" || parentPrincipal.Purpose != "deploy" {
		t.Fatalf("parent AuthenticateToken = %+v, %v", parentPrincipal, err)
	}

	parentCtx := authctx.WithPrincipal(ctx, *parentPrincipal)
	// Attenuation rejections:
	if _, _, _, _, err := auth.CreateAPIKey(parentCtx, storage.CreateAPIKeyInput{
		ActorID:       userID,
		AgentAccount:  "helper-bot",
		ParentKeyID:   parentKey.ID,
		HasSliceScope: true,
		SliceIDs:      []string{"sl_a"},
	}); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("minting for another subject with parent API key: err = %v", err)
	}
	if _, _, _, _, err := auth.CreateAPIKey(parentCtx, storage.CreateAPIKeyInput{
		ActorID:       userID,
		ParentKeyID:   parentKey.ID,
		HasSliceScope: true,
		SliceIDs:      []string{"sl_c"},
	}); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("widening slices: err = %v", err)
	}
	if _, _, _, _, err := auth.CreateAPIKey(parentCtx, storage.CreateAPIKeyInput{
		ActorID:     userID,
		ParentKeyID: parentKey.ID,
		ExpiresAt:   expParent.Add(time.Hour),
	}); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("extending expiry: err = %v", err)
	}

	// Narrowing child succeeds and inherits unspecified ceilings.
	childKey, childSecret, _, _, err := auth.CreateAPIKey(parentCtx, storage.CreateAPIKeyInput{
		ActorID:       userID,
		ParentKeyID:   parentKey.ID,
		Name:          "child-sub",
		HasSliceScope: true,
		SliceIDs:      []string{"sl_b"},
		MaxRole:       "contributor",
	})
	if err != nil {
		t.Fatalf("child CreateAPIKey = %v", err)
	}
	if childKey.ParentKeyID != parentKey.ID || childKey.ExpiresAt.IsZero() {
		t.Fatalf("child metadata = %+v", childKey)
	}
	grandchildKey, grandchildSecret, _, _, err := auth.CreateAPIKey(authctx.WithPrincipal(ctx, authctx.Principal{
		SubjectID: userID,
		KeyID:     childKey.ID,
	}), storage.CreateAPIKeyInput{
		ActorID:     userID,
		ParentKeyID: childKey.ID,
		Name:        "grandchild",
	})
	if err != nil {
		t.Fatalf("grandchild CreateAPIKey = %v", err)
	}
	if grandchildKey.MaxRole != "contributor" || len(grandchildKey.SliceIDs) != 1 || grandchildKey.SliceIDs[0] != "sl_b" {
		t.Fatalf("grandchild did not inherit limits: %+v", grandchildKey)
	}

	// Child key only lists itself and its descendants, and cannot revoke parent.
	childAuth, err := auth.AuthenticateToken(ctx, childSecret)
	if err != nil {
		t.Fatal(err)
	}
	childCtx := authctx.WithPrincipal(ctx, *childAuth)
	if _, _, visible, err := auth.ListAPIKeys(childCtx, userID, ""); err != nil || len(visible) != 2 {
		t.Fatalf("child ListAPIKeys = %+v, %v; want 2 (child + grandchild)", visible, err)
	}
	if _, err := auth.RevokeAPIKey(childCtx, userID, "", parentKey.ID); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("child revoking parent = %v, want ErrUnauthorized", err)
	}

	// Simulate expired ancestor out-of-band: grandchild stops authenticating immediately.
	stores.backend.mu.Lock()
	stores.backend.apiKeyRecords[parentKey.ID].key.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	stores.backend.mu.Unlock()
	if _, err := auth.AuthenticateToken(ctx, grandchildSecret); !errors.Is(err, storage.ErrUnauthenticated) {
		t.Fatalf("grandchild with expired parent = %v, want ErrUnauthenticated", err)
	}
	stores.backend.mu.Lock()
	stores.backend.apiKeyRecords[parentKey.ID].key.ExpiresAt = expParent
	stores.backend.mu.Unlock()

	// Recursive revoke on parent invalidates parent, child, and grandchild.
	if _, err := auth.RevokeAPIKey(ctx, userID, "", parentKey.ID); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{parentSecret, childSecret, grandchildSecret} {
		if _, err := auth.AuthenticateToken(ctx, secret); !errors.Is(err, storage.ErrUnauthenticated) {
			t.Fatalf("revoked chain secret still authenticates: %v", err)
		}
	}

	// Other user cannot touch helper-bot.
	if _, _, _, _, err := auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:      otherUser,
		AgentAccount: "helper-bot",
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("stranger CreateAPIKey on helper-bot = %v, want ErrNotFound", err)
	}
}
