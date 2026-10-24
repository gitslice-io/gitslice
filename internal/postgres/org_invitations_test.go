package postgres

import (
	"errors"
	"testing"

	istorage "gitslice.io/gitslice/internal/storage"
)

func TestOrganizationInvitationsAndProfiles(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	auth := store.Auth()
	if err := auth.CreateOrganization(ctx, "labs", []string{"user_alice"}, "user_alice"); err != nil {
		t.Fatal(err)
	}
	if n, err := auth.CountOrganizationsCreatedBy(ctx, "user_alice"); err != nil || n != 1 {
		t.Fatalf("organizations created by alice = %d, %v", n, err)
	}

	first, err := auth.UpsertAccountInvitation(ctx, "labs", "user_bob", "reader", "user_alice")
	if err != nil || first.CreatedAt.IsZero() {
		t.Fatalf("invite = %+v, %v", first, err)
	}
	// Inviting again replaces the role; there is still one invitation.
	if _, err := auth.UpsertAccountInvitation(ctx, "labs", "user_bob", "writer", "user_alice"); err != nil {
		t.Fatal(err)
	}
	pending, err := auth.ListAccountInvitations(ctx, "labs")
	if err != nil || len(pending) != 1 || pending[0].Role != "writer" || pending[0].Account != "labs" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	mine, err := auth.ListSubjectInvitations(ctx, "user_bob")
	if err != nil || len(mine) != 1 {
		t.Fatalf("bob's invitations = %+v, %v", mine, err)
	}
	if _, err := auth.AccountRole(ctx, "user_bob", "labs"); err == nil {
		t.Fatal("an invitation must not be a membership")
	}

	role, err := auth.AcceptAccountInvitation(ctx, "labs", "user_bob")
	if err != nil || role != "writer" {
		t.Fatalf("accept = %q, %v", role, err)
	}
	if got, err := auth.AccountRole(ctx, "user_bob", "labs"); err != nil || got != "writer" {
		t.Fatalf("bob's role after accepting = %q, %v", got, err)
	}
	if _, err := auth.AcceptAccountInvitation(ctx, "labs", "user_bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("accepting twice = %v, want not found", err)
	}
	if _, err := auth.GetAccountInvitation(ctx, "labs", "user_bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invitation after accepting = %v, want not found", err)
	}

	if _, err := auth.UpsertAccountInvitation(ctx, "labs", "ci_bot", "reader", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if err := auth.DeleteAccountInvitation(ctx, "labs", "ci_bot"); err != nil {
		t.Fatal(err)
	}
	if err := auth.DeleteAccountInvitation(ctx, "labs", "ci_bot"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice = %v, want not found", err)
	}
	if _, err := auth.UpsertAccountInvitation(ctx, "nowhere", "ci_bot", "reader", "user_alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inviting to an unknown account = %v", err)
	}

	profile, err := auth.UpdateAccountProfile(ctx, "labs", istorage.AccountProfile{DisplayName: "Labs", Description: "Things.", Website: "https://labs.example.com"})
	if err != nil || profile.DisplayName != "Labs" || profile.Kind != "organization" || profile.CreatedAt.IsZero() {
		t.Fatalf("profile = %+v, %v", profile, err)
	}
	if _, err := auth.GetAccountProfile(ctx, "nowhere"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown profile = %v", err)
	}

	// A picture's key is kept until replaced, and each change returns the
	// key it replaced; profile edits leave it alone.
	if previous, err := auth.SetAccountAvatar(ctx, "labs", "avatars/labs/one.png"); err != nil || previous != "" {
		t.Fatalf("first avatar: previous %q, %v", previous, err)
	}
	if profile, err := auth.UpdateAccountProfile(ctx, "labs", istorage.AccountProfile{DisplayName: "Labs 2"}); err != nil || profile.AvatarKey != "avatars/labs/one.png" {
		t.Fatalf("after a profile edit = %+v, %v", profile, err)
	}
	if previous, err := auth.SetAccountAvatar(ctx, "labs", ""); err != nil || previous != "avatars/labs/one.png" {
		t.Fatalf("removing: previous %q, %v", previous, err)
	}
	if profile, err := auth.GetAccountProfile(ctx, "labs"); err != nil || profile.AvatarKey != "" {
		t.Fatalf("after removing = %+v, %v", profile, err)
	}
	if _, err := auth.SetAccountAvatar(ctx, "nowhere", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account avatar = %v", err)
	}
}
