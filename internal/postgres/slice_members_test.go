package postgres

import (
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestSliceMembersStore(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	// dave is outside acme, with an account of his own.
	if _, err := store.db.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at) values ('user_dave', 'agent', 'dave', now());
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into accounts(id, slug, kind, display_name, created_at, updated_at) values ('acct_dave', 'dave', 'personal', 'Dave Bot', now(), now());
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at) values ('acct_dave', 'user_dave', 'owner', now())
	`); err != nil {
		t.Fatal(err)
	}
	slices, auth := store.Slices(), store.Auth()

	if _, err := auth.SliceRole(ctx, "user_dave", "slice_acme_payment"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("role before sharing: %v", err)
	}
	if visible, err := slices.ListVisible(ctx, "acme", "user_dave", "", 50); err != nil || len(visible) != 0 {
		t.Fatalf("visible before sharing = %v, %v", visible, err)
	}

	member, err := slices.SetMember(ctx, "slice_acme_payment", "user_dave", "writer", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if member.Username != "dave" || member.DisplayName != "Dave Bot" || member.Kind != "agent" || member.Role != "writer" || member.AddedBy != "alice" || member.CreatedAt.IsZero() {
		t.Fatalf("member = %+v", member)
	}
	// Setting again changes the role and keeps who added them.
	if member, err = slices.SetMember(ctx, "slice_acme_payment", "user_dave", "admin", "bob"); err != nil || member.Role != "admin" || member.AddedBy != "alice" {
		t.Fatalf("changed member = %+v, %v", member, err)
	}
	if role, err := auth.SliceRole(ctx, "user_dave", "slice_acme_payment"); err != nil || role != "admin" {
		t.Fatalf("role = %q, %v", role, err)
	}
	members, err := slices.ListMembers(ctx, "slice_acme_payment")
	if err != nil || len(members) != 1 || members[0].SubjectID != "user_dave" {
		t.Fatalf("members = %+v, %v", members, err)
	}
	visible, err := slices.ListVisible(ctx, "acme", "user_dave", "", 50)
	if err != nil || len(visible) != 1 || visible[0].Ref.Slice != "payment" {
		t.Fatalf("visible = %+v, %v", visible, err)
	}
	shared, err := slices.ListShared(ctx, "user_dave")
	if err != nil || len(shared) != 1 || shared[0].Slice.Ref.Slice != "payment" || shared[0].Role != "admin" {
		t.Fatalf("shared = %+v, %v", shared, err)
	}

	if err := slices.RemoveMember(ctx, "slice_acme_payment", "user_dave"); err != nil {
		t.Fatal(err)
	}
	if err := slices.RemoveMember(ctx, "slice_acme_payment", "user_dave"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
	if shared, err := slices.ListShared(ctx, "user_dave"); err != nil || len(shared) != 0 {
		t.Fatalf("shared after removal = %+v, %v", shared, err)
	}
	// Deleting a slice takes its members with it.
	if _, err := slices.SetMember(ctx, "slice_acme_backend", "user_dave", "reader", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := slices.Delete(ctx, "slice_acme_backend"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SliceRole(ctx, "user_dave", "slice_acme_backend"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("role on a deleted slice: %v", err)
	}
}
