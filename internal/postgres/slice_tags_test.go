package postgres

import (
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSliceTagsListAccountTagsPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	insertPGPersonalUser(t, ctx, store, "user_guest", "guest", "Guest")

	payment, err := store.Slices().Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: "payment"})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := store.Slices().Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Slices().UpdateDefinition(ctx, "user_alice", payment.Id, payment.DefinitionHash, &corev1.SliceDefinition{
		IncludedPaths: payment.Definition.IncludedPaths,
		Visibility:    "public",
	}); err != nil {
		t.Fatalf("UpdateDefinition(payment public): %v", err)
	}
	if _, err := store.Slices().UpdateDefinition(ctx, "user_alice", backend.Id, backend.DefinitionHash, &corev1.SliceDefinition{
		IncludedPaths: backend.Definition.IncludedPaths,
		Visibility:    "private",
	}); err != nil {
		t.Fatalf("UpdateDefinition(backend private): %v", err)
	}
	ref, err := store.Repository().GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.Slices().CreateTag(ctx, storage.SliceTag{
		SliceID:  payment.Id,
		Name:     "v1.0.0",
		CommitID: ref.GetCommitId(),
	}); err != nil {
		t.Fatalf("CreateTag(payment): %v", err)
	}
	if _, _, err := store.Slices().CreateTag(ctx, storage.SliceTag{
		SliceID:  backend.Id,
		Name:     "v2.0.0",
		CommitID: ref.GetCommitId(),
	}); err != nil {
		t.Fatalf("CreateTag(backend): %v", err)
	}

	all, err := store.Slices().ListAccountTags(ctx, "acme", false, "")
	if err != nil {
		t.Fatalf("ListAccountTags(all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAccountTags(all) len = %d, want 2: %+v", len(all), all)
	}

	pub, err := store.Slices().ListAccountTags(ctx, "acme", true, "")
	if err != nil {
		t.Fatalf("ListAccountTags(publicOnly): %v", err)
	}
	if len(pub) != 1 || pub[0].Name != "v1.0.0" || pub[0].SliceSlug != "payment" {
		t.Fatalf("ListAccountTags(publicOnly) = %+v, want only payment/v1.0.0", pub)
	}

	if _, err := store.Slices().SetMember(ctx, backend.Id, "user_guest", "reader", "user_alice"); err != nil {
		t.Fatalf("SetMember(backend, guest): %v", err)
	}
	shared, err := store.Slices().ListAccountTags(ctx, "acme", true, "user_guest")
	if err != nil {
		t.Fatalf("ListAccountTags(sharedWith): %v", err)
	}
	if len(shared) != 2 {
		t.Fatalf("ListAccountTags(sharedWith) len = %d, want 2: %+v", len(shared), shared)
	}
}
