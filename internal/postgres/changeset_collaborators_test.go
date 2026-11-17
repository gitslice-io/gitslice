package postgres

import (
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestChangesetCollaboratorsAndHandoffStore(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	cs, err := store.Changesets().Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Title:          "collab",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Auth().EnsureAccountMember(ctx, "user_alice", "acme")
	changesets := store.Changesets()

	got, err := changesets.AddCollaborator(ctx, cs.Id, "user_bob", "user_alice")
	if err != nil || len(got.Collaborators) != 1 || got.Collaborators[0] != "user_bob" {
		t.Fatalf("AddCollaborator = %+v, %v", got, err)
	}
	// Adding twice, or adding the author, changes nothing.
	if got, err = changesets.AddCollaborator(ctx, cs.Id, "user_bob", "user_alice"); err != nil || len(got.Collaborators) != 1 {
		t.Fatalf("AddCollaborator twice = %+v, %v", got, err)
	}
	if got, err = changesets.AddCollaborator(ctx, cs.Id, "user_alice", "user_alice"); err != nil || len(got.Collaborators) != 1 {
		t.Fatalf("AddCollaborator author = %+v, %v", got, err)
	}
	// Collaborators show on Get and List.
	listed, _, err := changesets.List(ctx, storage.ChangesetListFilter{Slice: cs.AuthoringSlice})
	if err != nil || len(listed) == 0 || len(listed[0].Collaborators) != 1 {
		t.Fatalf("List collaborators = %+v, %v", listed, err)
	}

	handed, err := changesets.Handoff(ctx, cs.Id, "user_bob")
	if err != nil || handed.Author != "user_bob" || handed.OriginalAuthor != "user_alice" || len(handed.Collaborators) != 0 {
		t.Fatalf("Handoff = %+v, %v", handed, err)
	}
	carol, err := store.Auth().EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_carol_handoff", "carol@example.com")
	if err != nil {
		t.Fatal(err)
	}
	handed, err = changesets.Handoff(ctx, cs.Id, carol)
	if err != nil || handed.Author != carol || handed.OriginalAuthor != "user_alice" {
		t.Fatalf("second Handoff = %+v, %v", handed, err)
	}

	if _, err := changesets.AddCollaborator(ctx, cs.Id, "", "user_alice"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("AddCollaborator empty = %v, want ErrInvalid", err)
	}
	if err := changesets.Abandon(ctx, cs.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := changesets.AddCollaborator(ctx, cs.Id, "user_bob", "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("AddCollaborator on abandoned = %v, want ErrConflict", err)
	}
	if _, err := changesets.Handoff(ctx, cs.Id, "user_bob"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Handoff on abandoned = %v, want ErrConflict", err)
	}
}
