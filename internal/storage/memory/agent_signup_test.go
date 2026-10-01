package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestRegisterAgentProvisionsAccountAndKey(t *testing.T) {
	ctx := context.Background()
	stores := New()

	agent, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:    "Release_Bot",
		OwnerEmail:  " Owner@Example.com ",
		DisplayName: "Release bot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Account != "release-bot" {
		t.Fatalf("account = %q; want release-bot", agent.Account)
	}
	if !strings.HasPrefix(agent.SubjectID, "agent_") {
		t.Fatalf("subject id = %q; want agent_ prefix", agent.SubjectID)
	}
	if !strings.HasPrefix(agent.APIKey, storage.APIKeyPrefix) {
		t.Fatalf("api key = %q; want %s prefix", agent.APIKey, storage.APIKeyPrefix)
	}

	role, err := stores.Auth.AccountRole(ctx, agent.SubjectID, "release-bot")
	if err != nil || role != "admin" {
		t.Fatalf("AccountRole = %q, %v; want admin", role, err)
	}
	if _, err := stores.Slices.Resolve(ctx, &corev1.SliceRef{Account: "release-bot", Slice: "home"}); err != nil {
		t.Fatalf("home slice: %v", err)
	}
	reg := stores.backend.agentRegistrations[agent.SubjectID]
	if reg.ownerEmail != "owner@example.com" || reg.account != "release-bot" {
		t.Fatalf("registration = %+v", reg)
	}

	subject, err := stores.Auth.SubjectForToken(ctx, agent.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	if subject.ID != agent.SubjectID || subject.DisplayName != "Release bot" {
		t.Fatalf("SubjectForToken = %+v; want %s / Release bot", subject, agent.SubjectID)
	}
	if _, err := stores.Auth.SubjectForToken(ctx, storage.APIKeyPrefix+"unknown"); !errors.Is(err, storage.ErrUnauthenticated) {
		t.Fatalf("unknown key err = %v; want ErrUnauthenticated", err)
	}
}

func TestRegisterAgentRejectsTakenUsernameAndBadEmail(t *testing.T) {
	ctx := context.Background()
	stores := New()

	if _, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "buildbot", OwnerEmail: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "buildbot", OwnerEmail: "a@example.com"}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate username err = %v; want ErrConflict", err)
	}
	if _, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "buildbot2", OwnerEmail: "not-an-email"}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("bad email err = %v; want ErrInvalid", err)
	}
	if _, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "!!", OwnerEmail: "a@example.com"}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("bad username err = %v; want ErrInvalid", err)
	}
}

func TestRegisterAgentResumesWithSameToken(t *testing.T) {
	ctx := context.Background()
	stores := New()
	token := strings.Repeat("r", storage.MinRegistrationTokenLength)
	in := storage.RegisterAgentInput{Username: "resume-bot", OwnerEmail: "owner@example.com", RegistrationToken: token}

	first, err := stores.Auth.RegisterAgent(ctx, in)
	if err != nil || first.Resumed {
		t.Fatalf("first RegisterAgent = %+v, %v", first, err)
	}
	// The response was "lost": retry with the same token resumes the agent.
	second, err := stores.Auth.RegisterAgent(ctx, in)
	if err != nil || !second.Resumed || second.SubjectID != first.SubjectID || second.APIKey == first.APIKey {
		t.Fatalf("retry = %+v, %v; want same agent, new key", second, err)
	}
	if _, err := stores.Auth.SubjectForToken(ctx, first.APIKey); !errors.Is(err, storage.ErrUnauthenticated) {
		t.Fatalf("undelivered first key err = %v; want revoked", err)
	}
	if subject, err := stores.Auth.SubjectForToken(ctx, second.APIKey); err != nil || subject.ID != first.SubjectID {
		t.Fatalf("resumed key = %+v, %v", subject, err)
	}

	other := in
	other.Username = "other-bot"
	if _, err := stores.Auth.RegisterAgent(ctx, other); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("same token, different username err = %v; want ErrConflict", err)
	}
	short := storage.RegisterAgentInput{Username: "short-bot", OwnerEmail: "owner@example.com", RegistrationToken: "too-short"}
	if _, err := stores.Auth.RegisterAgent(ctx, short); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("short token err = %v; want ErrInvalid", err)
	}
}
