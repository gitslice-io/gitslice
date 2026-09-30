package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/gitslice-io/gitslice/internal/storage"
)

func TestAgentClaimFlow(t *testing.T) {
	ctx := context.Background()
	stores := New()
	agent, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "release-bot", OwnerEmail: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	human, err := stores.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if provider, externalID, err := stores.Auth.ExternalIdentity(ctx, human); err != nil || provider != storage.ProviderClerk || externalID != "clerk_owner" {
		t.Fatalf("ExternalIdentity = %q, %q, %v", provider, externalID, err)
	}

	// No verified email yet: nothing to claim, and accepting fails closed.
	if claims, err := stores.Auth.ListPendingClaims(ctx, human); err != nil || len(claims) != 0 {
		t.Fatalf("claims before verification = %v, %v", claims, err)
	}
	if _, err := stores.Auth.AcceptClaim(ctx, human, agent.SubjectID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("accept before verification err = %v; want ErrNotFound", err)
	}

	if err := stores.Auth.SetVerifiedEmails(ctx, human, storage.ProviderClerk, []string{"Owner@Example.com", "bad"}); err != nil {
		t.Fatal(err)
	}
	claims, err := stores.Auth.ListPendingClaims(ctx, human)
	if err != nil || len(claims) != 1 || claims[0].AgentSubjectID != agent.SubjectID || claims[0].Account != "release-bot" {
		t.Fatalf("claims = %+v, %v", claims, err)
	}

	account, err := stores.Auth.AcceptClaim(ctx, human, agent.SubjectID)
	if err != nil || account != "release-bot" {
		t.Fatalf("AcceptClaim = %q, %v", account, err)
	}
	if role, err := stores.Auth.AccountRole(ctx, human, "release-bot"); err != nil || role != "owner" {
		t.Fatalf("human role = %q, %v; want owner", role, err)
	}
	if role, err := stores.Auth.AccountRole(ctx, agent.SubjectID, "release-bot"); err != nil || role != "admin" {
		t.Fatalf("agent role = %q, %v; want admin (co-owned)", role, err)
	}
	if _, err := stores.Auth.AcceptClaim(ctx, human, agent.SubjectID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second accept err = %v; want ErrNotFound", err)
	}
	if claims, _ := stores.Auth.ListPendingClaims(ctx, human); len(claims) != 0 {
		t.Fatalf("claimed agent still listed: %+v", claims)
	}

	// The claimed account is not the human's own personal account.
	if usernames, _ := stores.Auth.UsernamesForSubjects(ctx, []string{human}); usernames[human] != "" {
		t.Fatalf("human username = %q; want none before ChooseUsername", usernames[human])
	}
	if _, err := stores.Auth.ChooseUsername(ctx, human, "release-bot"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("choosing the agent's username err = %v; want ErrConflict", err)
	}
	if got, err := stores.Auth.ChooseUsername(ctx, human, "owner-human"); err != nil || got != "owner-human" {
		t.Fatalf("ChooseUsername = %q, %v", got, err)
	}
	slugs, err := stores.Auth.ListSubjectAccountSlugs(ctx, human)
	if err != nil || len(slugs) != 2 || slugs[0] != "owner-human" {
		t.Fatalf("human accounts = %v, %v; want owner-human first", slugs, err)
	}
}

func TestSetVerifiedEmailsReplacesPerSource(t *testing.T) {
	ctx := context.Background()
	stores := New()
	if _, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{Username: "old-bot", OwnerEmail: "old@example.com"}); err != nil {
		t.Fatal(err)
	}
	human, _ := stores.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_h", "")
	_ = stores.Auth.SetVerifiedEmails(ctx, human, storage.ProviderClerk, []string{"old@example.com"})
	_ = stores.Auth.SetVerifiedEmails(ctx, human, storage.ProviderClerk, []string{"new@example.com"})
	if claims, _ := stores.Auth.ListPendingClaims(ctx, human); len(claims) != 0 {
		t.Fatalf("an email removed at the provider must stop matching: %+v", claims)
	}
}
