package memory

import (
	"context"
	"errors"
	"slices"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestRoleValidationAndOrdering(t *testing.T) {
	ctx := context.Background()
	stores := New()

	// Role ordering: reader < contributor < member/writer < admin < owner, and no maintainer.
	if slices.Contains(storage.AccountRoles, "maintainer") || slices.Contains(storage.SliceRoles, "maintainer") {
		t.Fatalf("maintainer role must not exist: account=%v slice=%v", storage.AccountRoles, storage.SliceRoles)
	}
	if !(storage.AccountRoleRank("owner") < storage.AccountRoleRank("admin") &&
		storage.AccountRoleRank("admin") < storage.AccountRoleRank("writer") &&
		storage.AccountRoleRank("writer") < storage.AccountRoleRank("member") &&
		storage.AccountRoleRank("member") < storage.AccountRoleRank("contributor") &&
		storage.AccountRoleRank("contributor") < storage.AccountRoleRank("reader") &&
		storage.AccountRoleRank("reader") < storage.AccountRoleRank("maintainer")) {
		t.Fatalf("unexpected AccountRoleRank order")
	}
	if !(storage.SliceRoleRank("admin") < storage.SliceRoleRank("writer") &&
		storage.SliceRoleRank("writer") < storage.SliceRoleRank("contributor") &&
		storage.SliceRoleRank("contributor") < storage.SliceRoleRank("reader") &&
		storage.SliceRoleRank("reader") < storage.SliceRoleRank("owner") &&
		storage.SliceRoleRank("reader") < storage.SliceRoleRank("maintainer")) {
		t.Fatalf("unexpected SliceRoleRank order")
	}

	stores.AddPersonalAccount("user_owner", "owner")
	stores.AddPersonalAccount("user_contrib", "contrib")
	stores.AddPersonalAccount("user_reader", "reader")
	if err := stores.Auth.CreateOrganization(ctx, "acme", []string{"user_owner"}, "user_owner"); err != nil {
		t.Fatal(err)
	}
	slice := stores.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	// Reject maintainer and unknown roles across account members, invitations, and slice members.
	for _, invalid := range []string{"maintainer", "", "superuser"} {
		if err := stores.Auth.SetAccountMemberRole(ctx, "acme", "user_contrib", invalid); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("SetAccountMemberRole(%q) = %v, want ErrInvalid", invalid, err)
		}
		if _, err := stores.Auth.UpsertAccountInvitation(ctx, "acme", "user_contrib", invalid, "user_owner"); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("UpsertAccountInvitation(%q) = %v, want ErrInvalid", invalid, err)
		}
		if _, err := stores.Slices.SetMember(ctx, slice.Id, "user_contrib", invalid, "owner"); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("SetMember(%q) = %v, want ErrInvalid", invalid, err)
		}
	}

	// Accept contributor (with normalization) and sort contributor between member and reader.
	if err := stores.Auth.SetAccountMemberRole(ctx, "acme", "user_contrib", " CONTRIBUTOR "); err != nil {
		t.Fatalf("SetAccountMemberRole contributor: %v", err)
	}
	if err := stores.Auth.SetAccountMemberRole(ctx, "acme", "user_reader", "reader"); err != nil {
		t.Fatalf("SetAccountMemberRole reader: %v", err)
	}
	members, err := stores.Auth.ListAccountMembers(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 || members[0].Role != "owner" || members[1].Role != "contributor" || members[2].Role != "reader" {
		t.Fatalf("ListAccountMembers order = %+v, want owner, contributor, reader", members)
	}

	inv, err := stores.Auth.UpsertAccountInvitation(ctx, "acme", "user_invitee", "contributor", "user_owner")
	if err != nil || inv.Role != "contributor" {
		t.Fatalf("UpsertAccountInvitation contributor = %+v, %v", inv, err)
	}

	sm, err := stores.Slices.SetMember(ctx, slice.Id, "user_contrib", " Contributor ", "owner")
	if err != nil || sm.Role != "contributor" {
		t.Fatalf("SetMember contributor = %+v, %v", sm, err)
	}
}
