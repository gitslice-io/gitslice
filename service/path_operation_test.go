package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newPathOperationFixture has alice writing acme/payment, which requires an
// approval and a check, and a globex/docs slice alice can't write.
func newPathOperationFixture(t *testing.T, globexVisibility string) (context.Context, *memory.Stores, *Handlers) {
	t.Helper()
	mem, handlers := newMemoryHandlers()
	mem.AddAccount("user_bob", "globex")
	mem.PutSliceWithSubmitSettings(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private", 1, []string{"unit"})
	mem.PutSlice(&corev1.SliceRef{Account: "globex", Slice: "docs"}, []string{"/globex/docs"}, globexVisibility)
	file := func(p, hash string, mode uint32) storage.FileEntry {
		return storage.FileEntry{Path: p, BlobID: "blob_" + hash, ContentHash: "sha256:" + hash, Mode: mode, Size: 1}
	}
	mem.PutCommitWithFiles("commit_seed", []storage.FileEntry{
		file("/acme/payment/lib/a.go", "a", 0o100644),
		file("/acme/payment/lib/sub/b.sh", "b", 0o100755),
		file("/acme/payment/main.go", "m", 0o100644),
		file("/globex/docs/guide/intro.md", "g", 0o100644),
	}, nil)
	return authctx.WithSubjectID(context.Background(), "user_alice"), mem, handlers
}

func entryAtHead(t *testing.T, ctx context.Context, handlers *Handlers, p string) *storage.TreeEntry {
	t.Helper()
	ref, err := handlers.Changeset.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := handlers.Changeset.Repository.GetEntry(ctx, ref.CommitId, p)
	if err != nil {
		return nil
	}
	return entry
}

func wantPathOpCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

var pathOpSlice = &corev1.SliceRef{Account: "acme", Slice: "payment"}

func TestCopyPathLandsAFolderWithoutTheSlicesRequirements(t *testing.T) {
	ctx, _, handlers := newPathOperationFixture(t, "private")

	res, err := handlers.Changeset.CopyPath(ctx, &corev1.CopyPathRequest{
		Slice:           pathOpSlice,
		SourcePath:      "/acme/payment/lib",
		DestinationPath: "/acme/payment/vendor/lib",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetCommitId() == "" || res.GetChangeset().GetStatus() != "submitted" {
		t.Fatalf("response = %#v, want a published commit", res)
	}
	if got := res.GetChangeset().GetTitle(); got != "Copy /acme/payment/lib to /acme/payment/vendor/lib" {
		t.Fatalf("title = %q", got)
	}
	for _, p := range []string{"/acme/payment/lib/a.go", "/acme/payment/vendor/lib/a.go", "/acme/payment/vendor/lib/sub/b.sh"} {
		if entryAtHead(t, ctx, handlers, p) == nil {
			t.Fatalf("%s is missing after the copy", p)
		}
	}
	if got := entryAtHead(t, ctx, handlers, "/acme/payment/vendor/lib/sub/b.sh"); got.Mode != 0o100755 {
		t.Fatalf("copied mode = %o, want 100755", got.Mode)
	}
}

func TestMovePathRenamesWithinTheSlice(t *testing.T) {
	ctx, _, handlers := newPathOperationFixture(t, "private")

	if _, err := handlers.Changeset.MovePath(ctx, &corev1.MovePathRequest{
		Slice:           pathOpSlice,
		SourcePath:      "/acme/payment/main.go",
		DestinationPath: "/acme/payment/cmd/main.go",
	}); err != nil {
		t.Fatal(err)
	}
	if entryAtHead(t, ctx, handlers, "/acme/payment/main.go") != nil {
		t.Fatal("source still exists after the move")
	}
	if entryAtHead(t, ctx, handlers, "/acme/payment/cmd/main.go") == nil {
		t.Fatal("destination is missing after the move")
	}
}

func TestPathOperationsRefuseBadTargets(t *testing.T) {
	ctx, _, handlers := newPathOperationFixture(t, "private")
	copyTo := func(source, destination string) error {
		_, err := handlers.Changeset.CopyPath(ctx, &corev1.CopyPathRequest{Slice: pathOpSlice, SourcePath: source, DestinationPath: destination})
		return err
	}
	moveTo := func(source, destination string) error {
		_, err := handlers.Changeset.MovePath(ctx, &corev1.MovePathRequest{Slice: pathOpSlice, SourcePath: source, DestinationPath: destination})
		return err
	}

	wantPathOpCode(t, copyTo("/acme/payment/lib", "/acme/payment/main.go"), codes.AlreadyExists)
	wantPathOpCode(t, copyTo("/acme/payment/missing", "/acme/payment/x"), codes.NotFound)
	wantPathOpCode(t, copyTo("/acme/payment/lib", "/acme/payment/lib/again"), codes.InvalidArgument)
	wantPathOpCode(t, copyTo("/acme/payment/lib", "/acme/other/lib"), codes.FailedPrecondition)
	wantPathOpCode(t, copyTo("/acme/payment/lib", "/acme/payment"), codes.FailedPrecondition)
	wantPathOpCode(t, moveTo("/acme/payment/lib", "/acme/payment/lib/inner"), codes.InvalidArgument)
	wantPathOpCode(t, moveTo("/acme/payment", "/acme/payment/x"), codes.FailedPrecondition)
	wantPathOpCode(t, moveTo("/globex/docs/guide", "/acme/payment/guide"), codes.FailedPrecondition)
	if entryAtHead(t, ctx, handlers, "/acme/payment/lib/again") != nil || entryAtHead(t, ctx, handlers, "/acme/payment/guide") != nil {
		t.Fatal("a refused operation changed the tree")
	}
}

func TestCopyPathReadsAnotherAccountOnlyThroughAReadableSlice(t *testing.T) {
	globex := &corev1.SliceRef{Account: "globex", Slice: "docs"}

	t.Run("public source", func(t *testing.T) {
		ctx, _, handlers := newPathOperationFixture(t, "public")
		_, err := handlers.Changeset.CopyPath(ctx, &corev1.CopyPathRequest{Slice: pathOpSlice, SourcePath: "/globex/docs/guide", DestinationPath: "/acme/payment/guide"})
		wantPathOpCode(t, err, codes.InvalidArgument) // source_slice is required

		if _, err := handlers.Changeset.CopyPath(ctx, &corev1.CopyPathRequest{Slice: pathOpSlice, SourceSlice: globex, SourcePath: "/globex/docs/guide", DestinationPath: "/acme/payment/guide"}); err != nil {
			t.Fatal(err)
		}
		if entryAtHead(t, ctx, handlers, "/acme/payment/guide/intro.md") == nil {
			t.Fatal("cross-account copy is missing")
		}
		if entryAtHead(t, ctx, handlers, "/globex/docs/guide/intro.md") == nil {
			t.Fatal("cross-account copy removed the source")
		}
	})

	t.Run("private source", func(t *testing.T) {
		ctx, _, handlers := newPathOperationFixture(t, "private")
		_, err := handlers.Changeset.CopyPath(ctx, &corev1.CopyPathRequest{Slice: pathOpSlice, SourceSlice: globex, SourcePath: "/globex/docs/guide", DestinationPath: "/acme/payment/guide"})
		if code := status.Code(err); code != codes.PermissionDenied && code != codes.NotFound {
			t.Fatalf("error = %v, want PermissionDenied or NotFound", err)
		}
	})

	t.Run("destination outside writable slice", func(t *testing.T) {
		ctx, _, handlers := newPathOperationFixture(t, "public")
		_, err := handlers.Changeset.CopyPath(ctx, &corev1.CopyPathRequest{Slice: globex, SourcePath: "/globex/docs/guide", DestinationPath: "/globex/docs/guide2"})
		if code := status.Code(err); code != codes.PermissionDenied && code != codes.NotFound {
			t.Fatalf("error = %v, want PermissionDenied or NotFound", err)
		}
	})
}

func TestUpdateChangesetRefusesACopyFromOutsideTheSlice(t *testing.T) {
	ctx, _, handlers := newPathOperationFixture(t, "public")
	ref, err := handlers.Changeset.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{AuthoringSlice: pathOpSlice, BaseCommitId: ref.CommitId, Title: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{{Op: "copy", OldPath: "/globex/docs/guide", Path: "/acme/payment/guide"}},
	})
	wantPathOpCode(t, err, codes.FailedPrecondition)

	// Inside the slice, a copy edit goes through the normal review path.
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{{Op: "copy", OldPath: "/acme/payment/lib", Path: "/acme/payment/lib2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := patchset.GetChangedPaths(); len(got) != 1 || got[0] != "/acme/payment/lib2" {
		t.Fatalf("changed paths = %v, want only the destination", got)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id, ExpectedCurrentPatchsetId: patchset.Id}); err == nil {
		t.Fatal("a reviewed copy submitted without the slice's required approval")
	}
}
