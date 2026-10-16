package postgres

import (
	"errors"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSliceRenameKeepsTheOldNameWorking(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()
	created, err := slices.Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "bot-channels"}, []string{"/acme/bot-channels"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Repository().GetRef(ctx, DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := store.Changesets().Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "bot-channels"},
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "before the rename",
	})
	if err != nil {
		t.Fatal(err)
	}

	renamed, err := slices.Rename(ctx, created.Id, "botparley")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Id != created.Id || renamed.Ref.Slice != "botparley" || len(renamed.FormerNames) != 1 || renamed.FormerNames[0] != "bot-channels" {
		t.Fatalf("renamed = %v", renamed)
	}
	for _, name := range []string{"botparley", "bot-channels"} {
		got, err := slices.Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: name})
		if err != nil || got.Id != created.Id || got.Ref.Slice != "botparley" {
			t.Fatalf("Resolve(%s) = %v, %v; want the renamed slice", name, got, err)
		}
	}
	// The changeset follows the slice to its new name.
	moved, err := store.Changesets().Get(ctx, cs.Id)
	if err != nil || moved.AuthoringSlice.Slice != "botparley" {
		t.Fatalf("changeset authoring slice = %v, %v", moved.GetAuthoringSlice(), err)
	}
	listed, err := slices.List(ctx, "acme", "", 50, false)
	if err != nil || !sliceNamed(listed, "botparley") || sliceNamed(listed, "bot-channels") {
		t.Fatalf("List = %v, %v", listed, err)
	}

	// The old name is reserved: no new slice, and no rename to it by another slice.
	if _, err := slices.Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "bot-channels"}, []string{"/acme/bot-channels"}, "private", 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("creating a slice with a reserved name: err = %v, want ErrConflict", err)
	}
	other, err := slices.Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "other"}, []string{"/acme/other"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.Rename(ctx, other.Id, "bot-channels"); !errors.Is(err, ErrConflict) {
		t.Fatalf("renaming to a reserved name: err = %v, want ErrConflict", err)
	}
	if _, err := slices.Rename(ctx, other.Id, "payment"); !errors.Is(err, ErrConflict) {
		t.Fatalf("renaming to a name in use: err = %v, want ErrConflict", err)
	}
	if _, err := slices.Rename(ctx, other.Id, "Not A Name!"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("renaming to an invalid name: err = %v, want ErrInvalid", err)
	}
	if _, err := slices.Rename(ctx, other.Id, "other"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("renaming to the same name: err = %v, want ErrInvalid", err)
	}

	// A slice can take back one of its own earlier names.
	back, err := slices.Rename(ctx, created.Id, "bot-channels")
	if err != nil || back.Ref.Slice != "bot-channels" || len(back.FormerNames) != 1 || back.FormerNames[0] != "botparley" {
		t.Fatalf("rename back = %v, %v", back, err)
	}
}

func TestSliceArchiveHidesFromListings(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()
	created, err := slices.Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "old-ideas"}, []string{"/acme/old-ideas"}, "public", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := slices.SetArchived(ctx, created.Id, true)
	if err != nil || !archived.Archived {
		t.Fatalf("SetArchived = %v, %v", archived, err)
	}
	if again, err := slices.SetArchived(ctx, created.Id, true); err != nil || !again.Archived {
		t.Fatalf("archiving twice: %v, %v", again, err)
	}
	for name, list := range map[string]func(bool) ([]*corev1.Slice, error){
		"List":       func(all bool) ([]*corev1.Slice, error) { return slices.List(ctx, "acme", "", 100, all) },
		"ListPublic": func(all bool) ([]*corev1.Slice, error) { return slices.ListPublic(ctx, "acme", "", 100, all) },
		"ListVisible": func(all bool) ([]*corev1.Slice, error) {
			return slices.ListVisible(ctx, "acme", "user_dave", "", 100, all)
		},
	} {
		hidden, err := list(false)
		if err != nil || sliceNamed(hidden, "old-ideas") {
			t.Fatalf("%s hides archived slices: %v, %v", name, hidden, err)
		}
		shown, err := list(true)
		if err != nil || !sliceNamed(shown, "old-ideas") {
			t.Fatalf("%s with includeArchived: %v, %v", name, shown, err)
		}
	}
	// An archived slice still resolves, so its history stays readable.
	if got, err := slices.Resolve(ctx, created.Ref); err != nil || !got.Archived {
		t.Fatalf("Resolve archived = %v, %v", got, err)
	}
	restored, err := slices.SetArchived(ctx, created.Id, false)
	if err != nil || restored.Archived {
		t.Fatalf("unarchive = %v, %v", restored, err)
	}
	if listed, err := slices.List(ctx, "acme", "", 100, false); err != nil || !sliceNamed(listed, "old-ideas") {
		t.Fatalf("List after unarchive = %v, %v", listed, err)
	}
	if _, err := slices.SetArchived(ctx, "slice_missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archiving a missing slice: err = %v, want ErrNotFound", err)
	}
}

func TestSliceRetireKeepsHistoryAndTheName(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()
	created, err := slices.Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "scratch"}, []string{"/acme/scratch"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Repository().GetRef(ctx, DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := store.Changesets().Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: created.Ref,
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "scratch work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if total, open, err := slices.ChangesetCounts(ctx, created.Id); err != nil || total != 1 || open != 1 {
		t.Fatalf("counts = %d, %d, %v; want 1 open of 1", total, open, err)
	}
	if err := slices.Delete(ctx, created.Id); !errors.Is(err, ErrConflict) {
		t.Fatalf("Delete with changesets: err = %v, want ErrConflict", err)
	}
	if err := slices.Retire(ctx, created.Id); !errors.Is(err, ErrConflict) {
		t.Fatalf("Retire with an open changeset: err = %v, want ErrConflict", err)
	}

	if err := store.Changesets().Abandon(ctx, cs.Id); err != nil {
		t.Fatal(err)
	}
	if total, open, err := slices.ChangesetCounts(ctx, created.Id); err != nil || total != 1 || open != 0 {
		t.Fatalf("counts after abandoning = %d, %d, %v; want 0 open of 1", total, open, err)
	}
	if err := slices.Retire(ctx, created.Id); err != nil {
		t.Fatal(err)
	}

	if _, err := slices.Resolve(ctx, created.Ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve retired: err = %v, want ErrNotFound", err)
	}
	if listed, err := slices.List(ctx, "acme", "", 100, true); err != nil || sliceNamed(listed, "scratch") {
		t.Fatalf("List retired = %v, %v", listed, err)
	}
	if covering, err := slices.CoveringIDsByPath(ctx, []string{"/acme/scratch/file.txt"}); err != nil || len(covering["/acme/scratch/file.txt"]) != 0 {
		t.Fatalf("a retired slice must cover nothing: %v, %v", covering, err)
	}
	// The changeset and the slice row stay, so history still resolves.
	if kept, err := store.Changesets().Get(ctx, cs.Id); err != nil || kept.AuthoringSlice.Slice != "scratch" {
		t.Fatalf("changeset after retire = %v, %v", kept, err)
	}
	if byID, err := slices.Get(ctx, created.Id); err != nil || byID.Ref.Slice != "scratch" {
		t.Fatalf("Get retired by id = %v, %v", byID, err)
	}
	// The name stays taken, so a new slice cannot reuse the old id.
	if _, err := slices.Create(ctx, "user_alice", created.Ref, []string{"/acme/scratch"}, "private", 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("reusing a retired name: err = %v, want ErrConflict", err)
	}
	if err := slices.Retire(ctx, created.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retiring twice: err = %v, want ErrNotFound", err)
	}
}
