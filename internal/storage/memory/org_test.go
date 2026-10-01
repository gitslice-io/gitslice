package memory

import (
	"context"
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestOrganizationMembership(t *testing.T) {
	ctx := context.Background()
	stores := New()
	owner, err := stores.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Auth.ChooseUsername(ctx, owner, "org-owner"); err != nil {
		t.Fatal(err)
	}
	member, err := stores.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_member", "member@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Auth.ChooseUsername(ctx, member, "org-member"); err != nil {
		t.Fatal(err)
	}
	if got, err := stores.Auth.SubjectIDForUsername(ctx, "org-member"); err != nil || got != member {
		t.Fatalf("SubjectIDForUsername = %q, %v", got, err)
	}

	if err := stores.Auth.CreateOrganization(ctx, "gitslice", []string{owner}, owner); err != nil {
		t.Fatal(err)
	}
	if err := stores.Auth.CreateOrganization(ctx, "gitslice", []string{owner}, owner); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate organization: %v", err)
	}
	if kind, err := stores.Auth.AccountKind(ctx, "gitslice"); err != nil || kind != storage.AccountKindOrganization {
		t.Fatalf("AccountKind(gitslice) = %q, %v", kind, err)
	}
	if kind, err := stores.Auth.AccountKind(ctx, "org-member"); err != nil || kind != storage.AccountKindPersonal {
		t.Fatalf("AccountKind(org-member) = %q, %v", kind, err)
	}
	if _, err := stores.Slices.Resolve(ctx, &corev1.SliceRef{Account: "gitslice", Slice: "home"}); err != nil {
		t.Fatalf("organization home slice: %v", err)
	}

	if err := stores.Auth.SetAccountMemberRole(ctx, "gitslice", member, "writer"); err != nil {
		t.Fatal(err)
	}
	if role, err := stores.Auth.AccountRole(ctx, member, "gitslice"); err != nil || role != "writer" {
		t.Fatalf("AccountRole = %q, %v", role, err)
	}
	if err := stores.Auth.SetAccountMemberRole(ctx, "org-member", owner, "writer"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("personal account membership change: %v", err)
	}
	if err := stores.Auth.SetAccountMemberRole(ctx, "gitslice", owner, "admin"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("demoting the last owner: %v", err)
	}
	if err := stores.Auth.RemoveAccountMember(ctx, "gitslice", owner); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("removing the last owner: %v", err)
	}
	if role, err := stores.Auth.AccountRole(ctx, owner, "gitslice"); err != nil || role != "owner" {
		t.Fatalf("owner role after rejected changes = %q, %v", role, err)
	}
	members, err := stores.Auth.ListAccountMembers(ctx, "gitslice")
	if err != nil || len(members) != 2 || members[0].Username != "org-owner" || members[1].Role != "writer" {
		t.Fatalf("ListAccountMembers = %+v, %v", members, err)
	}
	if err := stores.Auth.RemoveAccountMember(ctx, "gitslice", member); err != nil {
		t.Fatal(err)
	}
	if err := stores.Auth.RemoveAccountMember(ctx, "gitslice", member); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("removing a non-member: %v", err)
	}
}
