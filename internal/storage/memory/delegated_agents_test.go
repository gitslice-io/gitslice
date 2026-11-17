package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestDelegatedAgentsAndConversationRolesMemory(t *testing.T) {
	ctx := context.Background()
	stores := New()
	auth := stores.Auth
	slices := stores.Slices
	agents := stores.Agents

	ownerID, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_alice", "alice@example.com")
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

	if _, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:   ownerID,
		Username:  "missing-slice-bot",
		SliceRole: "writer",
	}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("role without slice err = %v, want ErrInvalid", err)
	}

	res, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:     ownerID,
		Username:    "mem-bot",
		DisplayName: "Memory Bot",
		SliceID:     targetSlice.Id,
		SliceRole:   "writer",
	})
	if err != nil {
		t.Fatalf("CreateDelegatedAgent: %v", err)
	}
	if res.Account != "mem-bot" || res.Role != "writer" || !strings.HasPrefix(res.Secret, storage.APIKeyPrefix) {
		t.Fatalf("unexpected delegated agent result: %+v", res)
	}
	principal, err := auth.AuthenticateToken(ctx, res.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if principal.SubjectID != res.AgentSubjectID || principal.SubjectKind != "agent" || principal.ControllerSubjectID != ownerID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	members, err := slices.ListMembers(ctx, targetSlice.Id)
	if err != nil || len(members) != 1 || members[0].SubjectID != res.AgentSubjectID || members[0].Role != "writer" {
		t.Fatalf("ListMembers = %+v, %v", members, err)
	}
	owned, err := auth.ListOwnedAgents(ctx, ownerID)
	if err != nil || len(owned) != 1 || owned[0].AgentSubjectID != res.AgentSubjectID {
		t.Fatalf("ListOwnedAgents = %+v, %v", owned, err)
	}
	if _, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:  res.AgentSubjectID,
		Username: "nested-mem-bot",
	}); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("agent actor err = %v, want ErrUnauthorized", err)
	}
	if _, err := auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:  ownerID,
		Username: "mem-bot",
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate username err = %v, want ErrConflict", err)
	}

	daemon, err := agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: ownerID,
		Account:   "alice",
		Name:      "mem-daemon",
		Runtime:   "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	convDefault, err := agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  daemon.Id,
		SubjectID: ownerID,
		SliceID:   targetSlice.Id,
		Account:   "alice",
		SliceName: "app",
		Title:     "default",
	})
	if err != nil || convDefault.GetRole() != "contributor" {
		t.Fatalf("default conv = %+v, %v", convDefault, err)
	}
	convWriter, err := agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  daemon.Id,
		SubjectID: ownerID,
		SliceID:   targetSlice.Id,
		Account:   "alice",
		SliceName: "app",
		Title:     "writer",
		Role:      "writer",
	})
	if err != nil || convWriter.GetRole() != "writer" {
		t.Fatalf("writer conv = %+v, %v", convWriter, err)
	}
	if _, err := agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  daemon.Id,
		SubjectID: ownerID,
		SliceID:   targetSlice.Id,
		Account:   "alice",
		SliceName: "app",
		Title:     "bad",
		Role:      "admin",
	}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("invalid role conv err = %v, want ErrInvalid", err)
	}
}
