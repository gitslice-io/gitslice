package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestOwnerReplacesALostAgentKey(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	handlers.Auth.AgentSignupEnabled = true
	ctx := context.Background()
	agent, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "release-bot", OwnerEmail: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_stranger", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.SetVerifiedEmails(ctx, owner, storage.ProviderClerk, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	ownerCtx, strangerCtx := authctx.WithSubjectID(ctx, owner), authctx.WithSubjectID(ctx, stranger)
	agentCtx := authctx.WithSubjectID(ctx, agent.SubjectId)

	// Not yet an owner: not found, whoever asks.
	_, err = handlers.Auth.CreateApiKey(ownerCtx, &corev1.CreateApiKeyRequest{Agent: "release-bot"})
	wantCode(t, err, codes.NotFound, "an unclaimed agent's owner creating a key")
	if _, err := handlers.Auth.AcceptClaim(ownerCtx, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId}); err != nil {
		// The claim needs the Clerk email check in the service; accept at the store.
		if _, err := mem.Auth.AcceptClaim(ctx, owner, agent.SubjectId); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := handlers.Auth.ListApiKeys(ownerCtx, &corev1.ListApiKeysRequest{Agent: "release-bot"})
	if err != nil || listed.Account != "release-bot" || listed.AgentSubjectId != agent.SubjectId || len(listed.Keys) != 1 {
		t.Fatalf("ListApiKeys = %+v, %v", listed, err)
	}
	lost := listed.Keys[0]

	created, err := handlers.Auth.CreateApiKey(ownerCtx, &corev1.CreateApiKeyRequest{Agent: "release-bot", Name: "after the loss"})
	if err != nil || !strings.HasPrefix(created.Secret, storage.APIKeyPrefix) || created.Key.Name != "after the loss" || created.Key.RevokedAt != "" {
		t.Fatalf("CreateApiKey = %+v, %v", created, err)
	}
	if got, err := mem.Auth.SubjectForToken(ctx, created.Secret); err != nil || got.ID != agent.SubjectId {
		t.Fatalf("the new key should sign in as the agent: %+v, %v", got, err)
	}

	// Listing never carries a secret.
	after, err := handlers.Auth.ListApiKeys(agentCtx, &corev1.ListApiKeysRequest{})
	if err != nil || len(after.Keys) != 2 {
		t.Fatalf("the agent listing its own keys = %+v, %v", after, err)
	}
	if text := protojson.Format(after); strings.Contains(text, created.Secret) || strings.Contains(text, agent.ApiKey) {
		t.Fatalf("a key listing leaked a secret:\n%s", text)
	}

	revoked, err := handlers.Auth.RevokeApiKey(ownerCtx, &corev1.RevokeApiKeyRequest{Agent: "release-bot", KeyId: lost.Id})
	if err != nil || revoked.Key.RevokedAt == "" {
		t.Fatalf("RevokeApiKey = %+v, %v", revoked, err)
	}
	if _, err := mem.Auth.SubjectForToken(ctx, agent.ApiKey); err == nil {
		t.Fatal("the revoked key still signs in")
	}

	// A stranger gets nothing, and learns nothing about which accounts are agents.
	for name, call := range map[string]func() error{
		"list": func() error {
			_, err := handlers.Auth.ListApiKeys(strangerCtx, &corev1.ListApiKeysRequest{Agent: "release-bot"})
			return err
		},
		"create": func() error {
			_, err := handlers.Auth.CreateApiKey(strangerCtx, &corev1.CreateApiKeyRequest{Agent: "release-bot"})
			return err
		},
		"revoke": func() error {
			_, err := handlers.Auth.RevokeApiKey(strangerCtx, &corev1.RevokeApiKeyRequest{Agent: "release-bot", KeyId: created.Key.Id})
			return err
		},
		"yourself, not an agent": func() error {
			_, err := handlers.Auth.CreateApiKey(strangerCtx, &corev1.CreateApiKeyRequest{})
			return err
		},
	} {
		wantCode(t, call(), codes.NotFound, "stranger: "+name)
	}
	if _, err := mem.Auth.SubjectForToken(ctx, created.Secret); err != nil {
		t.Fatalf("the stranger's attempts must not touch the key: %v", err)
	}
	_, err = handlers.Auth.CreateApiKey(context.Background(), &corev1.CreateApiKeyRequest{})
	wantCode(t, err, codes.Unauthenticated, "no caller")
}
