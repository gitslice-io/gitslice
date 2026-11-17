package authz

import (
	"context"
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestAuthorizeReadVisibility(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_member", "acme", "member")
	authorizer := New(mem.Auth)

	if err := authorizer.Authorize(context.Background(), "", authzTestSlice("public"), ActionRead); err != nil {
		t.Fatalf("public read for anonymous subject = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_outsider", authzTestSlice("public"), ActionRead); err != nil {
		t.Fatalf("public read for outsider = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "", authzTestSlice("private"), ActionRead); !errors.Is(err, storage.ErrUnauthenticated) {
		t.Fatalf("private read for anonymous subject = %v, want unauthenticated", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_outsider", authzTestSlice("private"), ActionRead); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("private read for outsider = %v, want unauthorized", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_member", authzTestSlice("private"), ActionRead); err != nil {
		t.Fatalf("private read for member = %v, want nil", err)
	}
}

func TestAuthorizeRoleCapabilities(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_admin", "acme", "admin")
	mem.AddAccountRole("user_writer", "acme", "writer")
	mem.AddAccountRole("user_reader", "acme", "reader")
	authorizer := New(mem.Auth)
	slice := authzTestSlice("private")

	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionAdmin); err != nil {
		t.Fatalf("admin action for admin = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_writer", slice, ActionWrite); err != nil {
		t.Fatalf("write action for writer = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_writer", slice, ActionAdmin); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("admin action for writer = %v, want unauthorized", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_reader", slice, ActionWrite); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("write action for reader = %v, want unauthorized", err)
	}
}

func TestAuthorizeArchivedBlocksMutations(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_admin", "acme", "admin")
	authorizer := New(mem.Auth)
	slice := authzTestSlice("private")
	slice.Archived = true

	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionRead); err != nil {
		t.Fatalf("archived read for admin = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionWrite); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("archived write for admin = %v, want ErrConflict", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionAdmin); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("archived admin for admin = %v, want ErrConflict", err)
	}
	if err := authorizer.AuthorizeAllowArchived(context.Background(), "user_admin", slice, ActionAdmin); err != nil {
		t.Fatalf("AuthorizeAllowArchived for admin = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_outsider", slice, ActionAdmin); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("archived admin for outsider = %v, want ErrUnauthorized", err)
	}
}

func authzTestSlice(visibility string) *corev1.Slice {
	return &corev1.Slice{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment"},
			Visibility:    visibility,
		},
	}
}

func TestAuthorizeRoleCapabilityMatrix(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_owner", "acme", "owner")
	mem.AddAccountRole("user_admin", "acme", "admin")
	mem.AddAccountRole("user_writer", "acme", "writer")
	mem.AddAccountRole("user_member", "acme", "member")
	mem.AddAccountRole("user_contributor", "acme", "contributor")
	mem.AddAccountRole("user_reader", "acme", "reader")
	mem.AddAccountRole("user_maintainer", "acme", "maintainer")

	authorizer := New(mem.Auth)
	slice := authzTestSlice("private")
	ctx := context.Background()

	allActions := []Action{
		ActionRead,
		ActionPropose,
		ActionSubmit,
		ActionReview,
		ActionTag,
		ActionUseSecrets,
		ActionManageMembers,
		ActionActOnAnyChangeset,
		ActionWrite,
		ActionAdmin,
	}

	allowed := map[string]map[Action]bool{
		"user_owner": {
			ActionRead: true, ActionPropose: true, ActionSubmit: true, ActionReview: true,
			ActionTag: true, ActionUseSecrets: true, ActionManageMembers: true, ActionActOnAnyChangeset: true,
			ActionWrite: true, ActionAdmin: true,
		},
		"user_admin": {
			ActionRead: true, ActionPropose: true, ActionSubmit: true, ActionReview: true,
			ActionTag: true, ActionUseSecrets: true, ActionManageMembers: true, ActionActOnAnyChangeset: true,
			ActionWrite: true, ActionAdmin: true,
		},
		"user_writer": {
			ActionRead: true, ActionPropose: true, ActionSubmit: true, ActionReview: true,
			ActionTag: true, ActionUseSecrets: true, ActionWrite: true,
		},
		"user_member": {
			ActionRead: true, ActionPropose: true, ActionSubmit: true, ActionReview: true,
			ActionTag: true, ActionUseSecrets: true, ActionWrite: true,
		},
		"user_contributor": {
			ActionRead: true, ActionPropose: true,
		},
		"user_reader": {
			ActionRead: true,
		},
		"user_maintainer": {},
	}

	for subject, want := range allowed {
		for _, action := range allActions {
			err := authorizer.Authorize(ctx, subject, slice, action)
			if want[action] {
				if err != nil {
					t.Errorf("Authorize(%s, %s) = %v, want nil", subject, action, err)
				}
			} else if !errors.Is(err, storage.ErrUnauthorized) {
				t.Errorf("Authorize(%s, %s) = %v, want ErrUnauthorized", subject, action, err)
			}

			acctErr := authorizer.AuthorizeAccount(ctx, subject, "acme", action)
			if want[action] {
				if acctErr != nil {
					t.Errorf("AuthorizeAccount(%s, %s) = %v, want nil", subject, action, acctErr)
				}
			} else if !errors.Is(acctErr, storage.ErrUnauthorized) {
				t.Errorf("AuthorizeAccount(%s, %s) = %v, want ErrUnauthorized", subject, action, acctErr)
			}
		}
	}

	if err := authorizer.Authorize(ctx, "user_owner", slice, Action("unknown")); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("Authorize unknown action = %v, want ErrInvalid", err)
	}
	if err := authorizer.AuthorizeAccount(ctx, "user_owner", "acme", Action("unknown")); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("AuthorizeAccount unknown action = %v, want ErrInvalid", err)
	}
}

func TestAuthorizeArchivedBlocksAllNamedMutations(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_admin", "acme", "admin")
	authorizer := New(mem.Auth)
	slice := authzTestSlice("private")
	slice.Archived = true

	for _, action := range []Action{
		ActionPropose, ActionSubmit, ActionReview, ActionTag,
		ActionUseSecrets, ActionManageMembers, ActionActOnAnyChangeset,
		ActionWrite, ActionAdmin,
	} {
		if err := authorizer.Authorize(context.Background(), "user_admin", slice, action); !errors.Is(err, storage.ErrConflict) {
			t.Errorf("archived Authorize(%s) = %v, want ErrConflict", action, err)
		}
		if err := authorizer.AuthorizeAllowArchived(context.Background(), "user_admin", slice, action); err != nil {
			t.Errorf("archived AuthorizeAllowArchived(%s) = %v, want nil", action, err)
		}
	}
}

func TestEffectiveRoleCombinesAccountAndSliceRoles(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	mem.AddPersonalAccount("user_alice", "alice")
	mem.AddAccountRole("user_alice", "acme", "owner")
	slice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	authorizer := New(mem.Auth)

	if role, ok, err := authorizer.EffectiveRole(ctx, "", slice); err != nil || ok || role != "" {
		t.Fatalf("anonymous EffectiveRole = (%q, %v, %v), want empty", role, ok, err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_outsider", slice); err != nil || ok || role != "" {
		t.Fatalf("outsider EffectiveRole = (%q, %v, %v), want empty", role, ok, err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_alice", nil); err != nil || ok || role != "" {
		t.Fatalf("nil slice EffectiveRole = (%q, %v, %v), want empty", role, ok, err)
	}

	// Account reader + slice contributor -> contributor.
	mem.AddAccountRole("user_a", "acme", "reader")
	if _, err := mem.Slices.SetMember(ctx, slice.Id, "user_a", "contributor", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_a", slice); err != nil || !ok || role != "contributor" {
		t.Fatalf("reader+contributor EffectiveRole = (%q, %v, %v), want contributor", role, ok, err)
	}
	if err := authorizer.Authorize(ctx, "user_a", slice, ActionPropose); err != nil {
		t.Fatalf("slice contributor ActionPropose = %v, want nil", err)
	}
	if err := authorizer.Authorize(ctx, "user_a", slice, ActionSubmit); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("slice contributor ActionSubmit = %v, want ErrUnauthorized", err)
	}

	// Account contributor + slice writer -> writer.
	mem.AddAccountRole("user_b", "acme", "contributor")
	if _, err := mem.Slices.SetMember(ctx, slice.Id, "user_b", "writer", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_b", slice); err != nil || !ok || role != "writer" {
		t.Fatalf("contributor+writer EffectiveRole = (%q, %v, %v), want writer", role, ok, err)
	}
	if err := authorizer.Authorize(ctx, "user_b", slice, ActionSubmit); err != nil {
		t.Fatalf("contributor+writer ActionSubmit = %v, want nil", err)
	}

	// Account writer + slice reader -> writer (higher wins).
	mem.AddAccountRole("user_c", "acme", "writer")
	if _, err := mem.Slices.SetMember(ctx, slice.Id, "user_c", "reader", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_c", slice); err != nil || !ok || role != "writer" {
		t.Fatalf("writer+reader EffectiveRole = (%q, %v, %v), want writer", role, ok, err)
	}

	// Legacy account member + slice writer -> writer; member + slice contributor -> member.
	mem.AddAccountRole("user_d", "acme", "member")
	if _, err := mem.Slices.SetMember(ctx, slice.Id, "user_d", "contributor", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_d", slice); err != nil || !ok || role != "member" {
		t.Fatalf("member+contributor EffectiveRole = (%q, %v, %v), want member", role, ok, err)
	}
	if _, err := mem.Slices.SetMember(ctx, slice.Id, "user_d", "writer", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_d", slice); err != nil || !ok || role != "writer" {
		t.Fatalf("member+writer EffectiveRole = (%q, %v, %v), want writer", role, ok, err)
	}

	// Account owner + slice admin -> owner.
	if _, err := mem.Slices.SetMember(ctx, slice.Id, "user_alice", "admin", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_alice", slice); err != nil || !ok || role != "owner" {
		t.Fatalf("owner+admin EffectiveRole = (%q, %v, %v), want owner", role, ok, err)
	}

	// Slice-only contributor (not an account member).
	if _, err := mem.Slices.SetMember(ctx, slice.Id, "user_slice_contrib", "contributor", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := authorizer.EffectiveRole(ctx, "user_slice_contrib", slice); err != nil || !ok || role != "contributor" {
		t.Fatalf("slice-only contributor EffectiveRole = (%q, %v, %v), want contributor", role, ok, err)
	}
}
