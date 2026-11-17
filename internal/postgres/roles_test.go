package postgres

import (
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestRoleValidationAndOrdering(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	auth, slices := store.Auth(), store.Slices()

	// Reject maintainer and unknown roles on slice members, org members, and invitations.
	if err := auth.CreateOrganization(ctx, "labs", []string{"user_alice"}, "user_alice"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"maintainer", "", "superuser"} {
		if err := auth.SetAccountMemberRole(ctx, "labs", "user_bob", invalid); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("SetAccountMemberRole(%q) = %v, want ErrInvalid", invalid, err)
		}
		if _, err := auth.UpsertAccountInvitation(ctx, "labs", "user_bob", invalid, "user_alice"); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("UpsertAccountInvitation(%q) = %v, want ErrInvalid", invalid, err)
		}
		if _, err := slices.SetMember(ctx, "slice_acme_payment", "user_bob", invalid, "alice"); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("SetMember(%q) = %v, want ErrInvalid", invalid, err)
		}
	}

	// Verify contributor role ordering in AccountRole SQL and ListAccountMembers.
	if _, err := auth.UpsertAccountInvitation(ctx, "labs", "user_bob", "contributor", "user_alice"); err != nil {
		t.Fatalf("UpsertAccountInvitation contributor: %v", err)
	}
	if role, err := auth.AcceptAccountInvitation(ctx, "labs", "user_bob"); err != nil || role != "contributor" {
		t.Fatalf("AcceptAccountInvitation = %q, %v", role, err)
	}
	if err := auth.SetAccountMemberRole(ctx, "labs", "ci_bot", "reader"); err != nil {
		t.Fatalf("SetAccountMemberRole reader: %v", err)
	}
	members, err := auth.ListAccountMembers(ctx, "labs")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 || members[0].Role != "owner" || members[1].Role != "contributor" || members[2].Role != "reader" {
		t.Fatalf("ListAccountMembers order = %+v, want owner, contributor, reader", members)
	}

	// If multiple memberships exist in account_memberships, AccountRole ranks contributor above reader.
	if _, err := store.db.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		select id, 'ci_bot', 'contributor', now() from accounts where slug = 'labs'
		on conflict do nothing
	`); err != nil {
		t.Fatal(err)
	}
	if got, err := auth.AccountRole(ctx, "ci_bot", "labs"); err != nil || got != "contributor" {
		t.Fatalf("AccountRole(contributor+reader) = %q, %v, want contributor", got, err)
	}

	sm, err := slices.SetMember(ctx, "slice_acme_payment", "user_bob", "contributor", "alice")
	if err != nil || sm.Role != "contributor" {
		t.Fatalf("SetMember contributor = %+v, %v", sm, err)
	}
}
