package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestDelegatedAgentsAndConversationRolesPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	auth := store.Auth()
	slices := store.Slices()
	agents := store.Agents()

	ownerID, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_alice_delegated", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ChooseUsername(ctx, ownerID, "alice"); err != nil {
		t.Fatal(err)
	}
	targetSlice, err := slices.Create(ctx, ownerID, &corev1.SliceRef{Account: "alice", Slice: "app"}, []string{"/alice/app"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// role without slice is rejected before any agent row is inserted.
	if _, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:   ownerID,
		Username:  "orphaned-bot",
		SliceRole: "reader",
	}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("role without slice err = %v, want ErrInvalid", err)
	}
	if _, err := auth.GetAccountProfile(ctx, "orphaned-bot"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("orphaned-bot should not exist, err = %v", err)
	}

	// Create delegated agent without slice, then another with slice and default role.
	plain, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:     ownerID,
		Username:    "plain-bot",
		DisplayName: "Plain Bot",
	})
	if err != nil {
		t.Fatalf("CreateDelegatedAgent plain-bot: %v", err)
	}
	if plain.Account != "plain-bot" || plain.Key.ID == "" || !strings.HasPrefix(plain.Secret, storage.APIKeyPrefix) || plain.Role != "" {
		t.Fatalf("unexpected plain delegated agent result: %+v", plain)
	}
	principal, err := auth.AuthenticateToken(ctx, plain.Secret)
	if err != nil {
		t.Fatalf("AuthenticateToken plain-bot secret: %v", err)
	}
	if principal.SubjectID != plain.AgentSubjectID || principal.SubjectKind != "agent" || principal.ControllerSubjectID != ownerID || principal.KeyID != plain.Key.ID {
		t.Fatalf("principal = %+v, want agent %s controlled by %s with key %s", principal, plain.AgentSubjectID, ownerID, plain.Key.ID)
	}

	owned, err := auth.ListOwnedAgents(ctx, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].AgentSubjectID != plain.AgentSubjectID {
		t.Fatalf("ListOwnedAgents = %+v, want [%s]", owned, plain.AgentSubjectID)
	}

	// Non-user (agent) actor cannot create another delegated agent.
	if _, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:  plain.AgentSubjectID,
		Username: "nested-bot",
	}); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("agent actor CreateDelegatedAgent err = %v, want ErrUnauthorized", err)
	}

	// Duplicate username is rejected cleanly.
	if _, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:  ownerID,
		Username: "plain-bot",
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate username err = %v, want ErrConflict", err)
	}

	// Create delegated agent with slice membership (empty role defaults to contributor).
	scoped, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:  ownerID,
		Username: "scoped-bot",
		SliceID:  targetSlice.Id,
	})
	if err != nil {
		t.Fatalf("CreateDelegatedAgent scoped-bot: %v", err)
	}
	if scoped.Role != "contributor" {
		t.Fatalf("default slice role = %q, want contributor", scoped.Role)
	}
	members, err := slices.ListMembers(ctx, targetSlice.Id)
	if err != nil {
		t.Fatalf("slices.ListMembers: %v", err)
	}
	if len(members) != 1 || members[0].SubjectID != scoped.AgentSubjectID || members[0].Role != "contributor" {
		t.Fatalf("slice members = %+v, want contributor %s", members, scoped.AgentSubjectID)
	}

	// Conversation roles default to contributor and enforce DB check constraint.
	daemon, err := authRegisterTestDaemon(ctx, agents, ownerID, "alice", "devbox")
	if err != nil {
		t.Fatal(err)
	}
	convDefault, err := agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  daemon.Id,
		SubjectID: ownerID,
		SliceID:   targetSlice.Id,
		Account:   "alice",
		SliceName: "app",
		Title:     "default role",
	})
	if err != nil {
		t.Fatalf("CreateConversation default: %v", err)
	}
	if convDefault.GetRole() != "contributor" {
		t.Fatalf("default conversation role = %q, want contributor", convDefault.GetRole())
	}
	convReader, err := agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  daemon.Id,
		SubjectID: ownerID,
		SliceID:   targetSlice.Id,
		Account:   "alice",
		SliceName: "app",
		Title:     "reader role",
		Role:      "reader",
	})
	if err != nil {
		t.Fatalf("CreateConversation reader: %v", err)
	}
	if convReader.GetRole() != "reader" {
		t.Fatalf("reader conversation role = %q, want reader", convReader.GetRole())
	}
	got, err := agents.GetConversation(ctx, convReader.Id)
	if err != nil || got.GetRole() != "reader" {
		t.Fatalf("GetConversation = %+v, %v", got, err)
	}
	listed, err := agents.ListConversations(ctx, storage.ConversationFilter{DaemonID: daemon.Id})
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListConversations = %d, %v", len(listed), err)
	}
	if _, err := store.db.ExecContext(ctx, `update agent_conversations set role = 'admin' where id = $1`, convReader.Id); err == nil {
		t.Fatal("expected agent_conversations_role_check constraint to reject role='admin'")
	}
}

func authRegisterTestDaemon(ctx context.Context, agents storage.AgentStore, subjectID, account, name string) (*corev1.AgentDaemon, error) {
	return agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: subjectID,
		Account:   account,
		Name:      name,
		Runtime:   "codex",
	})
}
