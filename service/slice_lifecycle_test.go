package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lifecycleFixture commits one file under each of the given paths and returns
// the handlers, the head commit and the owner's context.
func lifecycleFixture(t *testing.T, files ...string) (*memory.Stores, *Handlers, string, context.Context) {
	t.Helper()
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	data := []byte("content\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	var entries []storage.FileEntry
	for _, p := range files {
		entries = append(entries, storage.FileEntry{Path: p, BlobID: objectid.BlobID(data), ContentHash: hash, Mode: 0o100644, Size: int64(len(data))})
	}
	mem.PutCommitWithFiles("commit_lifecycle", entries, files)
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return mem, handlers, ref.CommitId, ctx
}

func wantCodeContaining(t *testing.T, err error, code codes.Code, contains string) {
	t.Helper()
	if status.Code(err) != code || !strings.Contains(err.Error(), contains) {
		t.Fatalf("err = %v, want code %s containing %q", err, code, contains)
	}
}

func TestRenameSliceKeepsTheOldNameAndRefusesHomeAndOutsiders(t *testing.T) {
	mem, handlers, _, ctx := lifecycleFixture(t, "/acme/bot-channels/a.txt")
	mem.AddPersonalAccount("user_bob", "bob")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "bot-channels"}, []string{"/acme/bot-channels"}, "private")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "home"}, []string{"/acme"}, "private")
	old := &corev1.SliceRef{Account: "acme", Slice: "bot-channels"}

	_, err := handlers.Slice.RenameSlice(authctx.WithSubjectID(context.Background(), "user_bob"), &corev1.RenameSliceRequest{Slice: old, NewName: "botparley"})
	wantCodeContaining(t, err, codes.PermissionDenied, "")
	_, err = handlers.Slice.RenameSlice(ctx, &corev1.RenameSliceRequest{Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}, NewName: "main"})
	wantCodeContaining(t, err, codes.InvalidArgument, "home slice")
	_, err = handlers.Slice.RenameSlice(ctx, &corev1.RenameSliceRequest{Slice: old, NewName: "home"})
	wantCodeContaining(t, err, codes.FailedPrecondition, "already exists")
	_, err = handlers.Slice.RenameSlice(ctx, &corev1.RenameSliceRequest{Slice: old, NewName: "Not A Name!"})
	wantCodeContaining(t, err, codes.InvalidArgument, "")

	renamed, err := handlers.Slice.RenameSlice(ctx, &corev1.RenameSliceRequest{Slice: old, NewName: "botparley"})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Ref.Slice != "botparley" || strings.Join(renamed.FormerNames, ",") != "bot-channels" {
		t.Fatalf("renamed = %v", renamed)
	}
	for _, name := range []string{"botparley", "bot-channels"} {
		got, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: name}})
		if err != nil || got.Id != renamed.Id || got.Ref.Slice != "botparley" {
			t.Fatalf("ResolveSlice(%s) = %v, %v", name, got, err)
		}
	}
	// The old name is reserved against new slices.
	_, err = handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{Ref: old, IncludedPaths: []string{"/acme/bot-channels"}, Visibility: "private"})
	wantCodeContaining(t, err, codes.FailedPrecondition, "reserved")
}

func TestArchivedSliceIsHiddenAndReadOnly(t *testing.T) {
	mem, handlers, base, ctx := lifecycleFixture(t, "/acme/payment/a.txt")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	ref := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	newChangeset := func() error {
		_, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{AuthoringSlice: ref, TargetRef: storage.DefaultTargetRef, BaseCommitId: base, Title: "change"})
		return err
	}

	archived, err := handlers.Slice.SetSliceArchived(ctx, &corev1.SetSliceArchivedRequest{Slice: ref, Archived: true})
	if err != nil || !archived.Archived {
		t.Fatalf("archive = %v, %v", archived, err)
	}
	listed, err := handlers.Slice.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil || sliceListContains(listed.Slices, "payment") {
		t.Fatalf("ListSlices shows an archived slice: %v, %v", listed, err)
	}
	listed, err = handlers.Slice.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme", IncludeArchived: true})
	if err != nil || !sliceListContains(listed.Slices, "payment") {
		t.Fatalf("ListSlices with include_archived: %v, %v", listed, err)
	}
	if got, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: archived.Id}); err != nil || !got.Archived {
		t.Fatalf("an archived slice stays readable: %v, %v", got, err)
	}
	wantCodeContaining(t, newChangeset(), codes.FailedPrecondition, "archived")

	restored, err := handlers.Slice.SetSliceArchived(ctx, &corev1.SetSliceArchivedRequest{Slice: ref, Archived: false})
	if err != nil || restored.Archived {
		t.Fatalf("unarchive = %v, %v", restored, err)
	}
	if err := newChangeset(); err != nil {
		t.Fatalf("changeset after unarchive: %v", err)
	}
	_, err = handlers.Slice.SetSliceArchived(ctx, &corev1.SetSliceArchivedRequest{Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}, Archived: true})
	wantCodeContaining(t, err, codes.InvalidArgument, "home slice")
}

func TestDeleteSliceWithChangesetsKeepsHistory(t *testing.T) {
	mem, handlers, base, ctx := lifecycleFixture(t, "/acme/scratch/a.txt")
	scratch := &corev1.SliceRef{Account: "acme", Slice: "scratch"}
	mem.PutSlice(scratch, []string{"/acme/scratch"}, "private")
	slice, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: scratch})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{AuthoringSlice: scratch, TargetRef: storage.DefaultTargetRef, BaseCommitId: base, Title: "scratch work"})
	if err != nil {
		t.Fatal(err)
	}
	del := func() (*corev1.DeleteSliceResponse, error) {
		return handlers.Slice.DeleteSlice(ctx, &corev1.DeleteSliceRequest{SliceId: slice.Id})
	}

	_, err = del()
	wantCodeContaining(t, err, codes.FailedPrecondition, "open changeset")
	if _, err := handlers.Changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	// The path still holds a file, which lives on in the account's home slice.
	resp, err := del()
	if err != nil || !resp.HistoryKept {
		t.Fatalf("DeleteSlice = %v, %v; want history kept", resp, err)
	}
	_, err = handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: scratch})
	wantCodeContaining(t, err, codes.NotFound, "")
	listed, err := handlers.Slice.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme", IncludeArchived: true})
	if err != nil || sliceListContains(listed.Slices, "scratch") {
		t.Fatalf("a deleted slice is still listed: %v, %v", listed, err)
	}
	kept, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil || kept.Status != "abandoned" {
		t.Fatalf("the changeset must remain: %v, %v", kept, err)
	}
}

func TestDeleteSliceWithChangesetsAllowsPathsWithNoFiles(t *testing.T) {
	mem, handlers, base, ctx := lifecycleFixture(t, "/acme/other/a.txt")
	ghost := &corev1.SliceRef{Account: "acme", Slice: "ghost"}
	mem.PutSlice(ghost, []string{"/acme/ghost"}, "private")
	slice, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: ghost})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{AuthoringSlice: ghost, TargetRef: storage.DefaultTargetRef, BaseCommitId: base, Title: "moved away"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	resp, err := handlers.Slice.DeleteSlice(ctx, &corev1.DeleteSliceRequest{SliceId: slice.Id})
	if err != nil || !resp.HistoryKept {
		t.Fatalf("DeleteSlice = %v, %v; want history kept", resp, err)
	}
}

func TestDeleteSliceWithoutChangesetsIsRemovedForGood(t *testing.T) {
	mem, handlers, _, ctx := lifecycleFixture(t, "/acme/tmp/a.txt")
	tmp := &corev1.SliceRef{Account: "acme", Slice: "tmp"}
	mem.PutSlice(tmp, []string{"/acme/tmp"}, "private")
	slice, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: tmp})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := handlers.Slice.DeleteSlice(ctx, &corev1.DeleteSliceRequest{SliceId: slice.Id})
	if err != nil || resp.HistoryKept {
		t.Fatalf("DeleteSlice = %v, %v; want a plain delete", resp, err)
	}
	if _, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: slice.Id}); err == nil {
		t.Fatal("the slice should be gone")
	}
}

func TestSliceDescriptionCanBeSetByOwnersOnly(t *testing.T) {
	mem, handlers, _, ctx := lifecycleFixture(t, "/acme/games/a.txt")
	mem.AddPersonalAccount("user_bob", "bob")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "games"}, []string{"/acme/games"}, "public")
	ref := &corev1.SliceRef{Account: "acme", Slice: "games"}

	got, err := handlers.Slice.SetSliceDescription(ctx, &corev1.SetSliceDescriptionRequest{Slice: ref, Description: "  A mirror of MacNativeGames by ly05010419.\r\nCredit to the original authors.  "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "A mirror of MacNativeGames by ly05010419.\nCredit to the original authors." {
		t.Fatalf("description = %q", got.Description)
	}
	resolved, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: ref})
	if err != nil || resolved.Description != got.Description {
		t.Fatalf("resolved = %v, %v", resolved, err)
	}

	_, err = handlers.Slice.SetSliceDescription(authctx.WithSubjectID(context.Background(), "user_bob"), &corev1.SetSliceDescriptionRequest{Slice: ref, Description: "mine now"})
	wantCodeContaining(t, err, codes.PermissionDenied, "")
	_, err = handlers.Slice.SetSliceDescription(ctx, &corev1.SetSliceDescriptionRequest{Slice: ref, Description: strings.Repeat("é", maxSliceDescriptionRunes+1)})
	wantCodeContaining(t, err, codes.InvalidArgument, "limit is 500")
	_, err = handlers.Slice.SetSliceDescription(ctx, &corev1.SetSliceDescriptionRequest{Slice: ref, Description: "bell\a"})
	wantCodeContaining(t, err, codes.InvalidArgument, "control character")

	cleared, err := handlers.Slice.SetSliceDescription(ctx, &corev1.SetSliceDescriptionRequest{Slice: ref})
	if err != nil || cleared.Description != "" {
		t.Fatalf("clear = %v, %v", cleared, err)
	}
}

func TestCreateSliceWithDescription(t *testing.T) {
	_, handlers, _, ctx := lifecycleFixture(t, "/acme/games/a.txt")
	created, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "games"}, IncludedPaths: []string{"/acme/games"}, Visibility: "public", Description: "Games"})
	if err != nil || created.Description != "Games" {
		t.Fatalf("created = %v, %v", created, err)
	}
	_, err = handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "games2"}, IncludedPaths: []string{"/acme/games"}, Visibility: "public", Description: strings.Repeat("x", 501)})
	wantCodeContaining(t, err, codes.InvalidArgument, "limit is 500")
}
