package postgres

import (
	"errors"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
)

func TestScopedAndChildAPIKeysPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	auth := store.Auth()

	userID, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ChooseUsername(ctx, userID, "alice"); err != nil {
		t.Fatal(err)
	}
	agent, err := auth.RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:    "pg-bot",
		OwnerEmail:  "alice@example.com",
		DisplayName: "PG Bot",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Mark a controller on a separate agent row without agent_registrations
	// to verify controller access works directly via subjects.controller_subject_id.
	ctrlAgent, err := auth.RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:    "controlled-pg",
		OwnerEmail:  "someone-else@example.com",
		DisplayName: "Controlled",
	})
	if err != nil {
		t.Fatal(err)
	}
	controlledID := ctrlAgent.SubjectID
	if _, err := store.db.ExecContext(ctx, `update subjects set controller_subject_id = $1 where id = $2`, userID, controlledID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `delete from agent_registrations where subject_id = $1`, controlledID); err != nil {
		t.Fatal(err)
	}

	// User login session has KeyID == "".
	code, _, err := auth.StartCliLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CompleteCliLogin(ctx, code, userID); err != nil {
		t.Fatal(err)
	}
	_, sessToken, _, err := auth.PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	sessPrincipal, err := auth.AuthenticateToken(ctx, sessToken)
	if err != nil || sessPrincipal.SubjectID != userID || sessPrincipal.KeyID != "" {
		t.Fatalf("session AuthenticateToken = %+v, %v", sessPrincipal, err)
	}

	// Controller can mint keys for controlled agent.
	ctrlKey, _, ctrlTarget, ctrlAcct, err := auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:      userID,
		AgentAccount: "controlled-pg",
		Name:         "ctrl-key",
		MaxRole:      "writer",
	})
	if err != nil || ctrlTarget != controlledID || ctrlAcct != "controlled-pg" || ctrlKey.MaxRole != "writer" {
		t.Fatalf("controller CreateAPIKey = %+v target=%q acct=%q err=%v", ctrlKey, ctrlTarget, ctrlAcct, err)
	}
	_ = agent

	// User self-key with slice scope, max_role, expiry, purpose, daemon/conv ids.
	expParent := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	parentKey, parentSecret, targetID, acct, err := auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:        userID,
		Name:           "pg-parent",
		HasSliceScope:  true,
		SliceIDs:       []string{"sl_1", "sl_2"},
		MaxRole:        "writer",
		ExpiresAt:      expParent,
		Purpose:        "ci-build",
		DaemonID:       "daemon_1",
		ConversationID: "conv_1",
	})
	if err != nil || targetID != userID || acct != "alice" {
		t.Fatalf("user self CreateAPIKey = %+v target=%q acct=%q err=%v", parentKey, targetID, acct, err)
	}
	parentPrincipal, err := auth.AuthenticateToken(ctx, parentSecret)
	if err != nil || !parentPrincipal.HasSliceScope || len(parentPrincipal.SliceIDs) != 2 ||
		parentPrincipal.MaxRole != "writer" || parentPrincipal.DaemonID != "daemon_1" || parentPrincipal.ConversationID != "conv_1" {
		t.Fatalf("parent AuthenticateToken = %+v, %v", parentPrincipal, err)
	}

	// Attenuated child + grandchild.
	parentCtx := authctx.WithPrincipal(ctx, *parentPrincipal)
	childKey, childSecret, _, _, err := auth.CreateAPIKey(parentCtx, storage.CreateAPIKeyInput{
		ActorID:       userID,
		ParentKeyID:   parentKey.ID,
		Name:          "pg-child",
		HasSliceScope: true,
		SliceIDs:      []string{"sl_2"},
		MaxRole:       "contributor",
	})
	if err != nil {
		t.Fatalf("child CreateAPIKey = %v", err)
	}
	if childKey.ParentKeyID != parentKey.ID || childKey.DaemonID != "daemon_1" || childKey.ConversationID != "conv_1" || !childKey.ExpiresAt.Equal(expParent) {
		t.Fatalf("child inheritance = %+v (want expiry %s)", childKey, expParent)
	}
	// Reject widening slice, role, expiry, daemon_id, or different agent.
	for name, bad := range map[string]storage.CreateAPIKeyInput{
		"other_agent": {ActorID: userID, AgentAccount: "controlled-pg", ParentKeyID: parentKey.ID},
		"wide_slice":  {ActorID: userID, ParentKeyID: parentKey.ID, HasSliceScope: true, SliceIDs: []string{"sl_3"}},
		"wide_role":   {ActorID: userID, ParentKeyID: parentKey.ID, MaxRole: ""},
		"longer_exp":  {ActorID: userID, ParentKeyID: parentKey.ID, ExpiresAt: expParent.Add(time.Hour)},
		"diff_daemon": {ActorID: userID, ParentKeyID: parentKey.ID, DaemonID: "daemon_other"},
		"diff_convo":  {ActorID: userID, ParentKeyID: parentKey.ID, ConversationID: "conv_other"},
	} {
		// Parent key context with wide_role explicit test: note empty MaxRole inherits, whereas invalid/higher role rejected
		if name == "wide_role" {
			// Skip empty since empty inherits; test child (contributor) requesting writer.
			continue
		}
		if _, _, _, _, err := auth.CreateAPIKey(parentCtx, bad); !errors.Is(err, storage.ErrUnauthorized) {
			t.Fatalf("%s: err = %v, want ErrUnauthorized", name, err)
		}
	}
	childAuth, err := auth.AuthenticateToken(ctx, childSecret)
	if err != nil {
		t.Fatal(err)
	}
	childCtx := authctx.WithPrincipal(ctx, *childAuth)
	if _, _, _, _, err := auth.CreateAPIKey(childCtx, storage.CreateAPIKeyInput{
		ActorID:     userID,
		ParentKeyID: childKey.ID,
		MaxRole:     "writer",
	}); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("child widening max_role: err = %v, want ErrUnauthorized", err)
	}
	grandchildKey, grandchildSecret, _, _, err := auth.CreateAPIKey(childCtx, storage.CreateAPIKeyInput{
		ActorID:     userID,
		ParentKeyID: childKey.ID,
		Name:        "pg-grandchild",
	})
	if err != nil {
		t.Fatalf("grandchild CreateAPIKey = %v", err)
	}

	// Check constraint rejects invalid max_role in DB.
	if _, err := store.db.ExecContext(ctx, `
		insert into api_keys(id, subject_id, name, token_hash, max_role) values ('key_bad', $1, 'bad', 'hash_bad', 'admin')
	`, userID); err == nil {
		t.Fatal("expected check constraint failure for max_role='admin'")
	}

	// Out-of-band ancestor expiry rejects leaf immediately in AuthenticateToken.
	if _, err := store.db.ExecContext(ctx, `update api_keys set expires_at = now() - interval '1 minute' where id = $1`, parentKey.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateToken(ctx, grandchildSecret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("grandchild with expired parent = %v, want ErrUnauthenticated", err)
	}
	if _, err := store.db.ExecContext(ctx, `update api_keys set expires_at = $2 where id = $1`, parentKey.ID, expParent); err != nil {
		t.Fatal(err)
	}

	// ListAPIKeys / RevokeAPIKey under child key only exposes child + grandchild.
	if _, _, visible, err := auth.ListAPIKeys(childCtx, userID, ""); err != nil || len(visible) != 2 {
		t.Fatalf("child ListAPIKeys = %+v, %v; want 2", visible, err)
	}
	if _, err := auth.RevokeAPIKey(childCtx, userID, "", parentKey.ID); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("child revoking parent = %v, want ErrUnauthorized", err)
	}

	// Recursive revoke on parent invalidates parent, child, and grandchild.
	if _, err := auth.RevokeAPIKey(ctx, userID, "", parentKey.ID); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{parentSecret, childSecret, grandchildSecret} {
		if _, err := auth.AuthenticateToken(ctx, secret); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("revoked key chain secret still signs in: %v", err)
		}
	}
	_ = grandchildKey
}
