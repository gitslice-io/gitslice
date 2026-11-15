package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBundledCheckRunNonTerminalStatusRecordsErrored(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "bundled-invalid"}
	mem.PutSliceWithSubmitSettings(sliceRef, []string{"/acme/payment/bundled-invalid"}, "private", 0, []string{"unit"})

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package bundledinvalid\nconst V = 1\n"),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "bundled invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/bundled-invalid/change.go",
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
		BundledCheckRuns: []*corev1.BundledCheckRun{{
			Name:   "unit",
			Status: "running",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	runs, err := handlers.Check.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id, PatchsetId: patchset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs.Runs) != 1 {
		t.Fatalf("ListCheckRuns returned %d runs, want 1: %#v", len(runs.Runs), runs.Runs)
	}
	if got := runs.Runs[0]; got.Status != "errored" || got.Summary != `invalid bundled check status "running"` {
		t.Fatalf("bundled run = %#v, want errored invalid-status summary", got)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err == nil || !strings.Contains(err.Error(), `required check "unit" is failing`) {
		t.Fatalf("SubmitChangeset error = %v, want failing unit check", err)
	}
}

func TestSubmitRejectsPatchsetConflicts(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "conflicted sync",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		PatchsetKind: "sync",
		Conflicts: []*corev1.PatchsetConflict{{
			Path:          "/acme/conflict.txt",
			ConflictClass: "content",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "unresolved patchset conflicts") {
		t.Fatalf("SubmitChangeset error = %v, want unresolved conflict FailedPrecondition", err)
	}
}

func TestInMemoryPublishPendingPreservesAdmissionOrder(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	older, olderPatchset := createStandaloneChangesetWithPatchset(t, ctx, handlers, ref.CommitId, "older entry", "/acme/payment/order_older.go", "package payment\nconst OrderOlder = 1\n")
	newer, newerPatchset := createStandaloneChangesetWithPatchset(t, ctx, handlers, ref.CommitId, "newer entry", "/acme/payment/order_newer.go", "package payment\nconst OrderNewer = 1\n")

	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               newer.Id,
		ExpectedCurrentPatchsetId: newerPatchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               older.Id,
		ExpectedCurrentPatchsetId: olderPatchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if published, err := mem.Changesets.PublishPending(ctx, 10); err != nil || published != 2 {
		t.Fatalf("PublishPending = %d, %v; want 2, nil", published, err)
	}

	currentRef, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	finalCommit, err := handlers.Repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: currentRef.CommitId})
	if err != nil {
		t.Fatal(err)
	}
	if finalCommit.Message != "older entry" || len(finalCommit.ParentIds) != 1 {
		t.Fatalf("final commit = %#v, want older entry on top of admission chain", finalCommit)
	}
	parentCommit, err := handlers.Repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: finalCommit.ParentIds[0]})
	if err != nil {
		t.Fatal(err)
	}
	if parentCommit.Message != "newer entry" || len(parentCommit.ParentIds) != 1 || parentCommit.ParentIds[0] != ref.CommitId {
		t.Fatalf("parent commit = %#v, want newer entry first after base %s", parentCommit, ref.CommitId)
	}
}

func TestChangesetDiffListAndAbandonUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	oldData := []byte("old\n")
	oldHash := objectid.RawContentHash(oldData)
	mem.PutObject(filesystem.BlobKey(oldHash), oldData)
	mem.PutCommitWithFiles("commit_old", []storage.FileEntry{{
		Path:        "/acme/payment/value.txt",
		BlobID:      objectid.BlobID(oldData),
		ContentHash: oldHash,
		Mode:        0o100644,
		Size:        int64(len(oldData)),
	}}, []string{"/acme/payment/value.txt"})
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	newBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("new\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "update value",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/value.txt",
			BlobId:      newBlob.BlobId,
			ContentHash: newBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{ChangesetId: cs.Id, Patchset: patchset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-old") || !strings.Contains(diff.Diff, "+new") {
		t.Fatalf("diff did not include old/new content:\n%s", diff.Diff)
	}
	listed, err := handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Changesets) != 1 || listed.Changesets[0].Id != cs.Id {
		t.Fatalf("unexpected changeset list: %#v", listed.Changesets)
	}
	if _, err := handlers.Changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	abandoned, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.Status != "abandoned" {
		t.Fatalf("status = %q, want abandoned", abandoned.Status)
	}
}

func TestChangesetAuthorsResolveToPersonalUsernameInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), storage.ProviderClerk, "clerk_taylor", "taylor@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	if _, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "Taylor_Name"}); err != nil {
		t.Fatal(err)
	}

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "taylor-name", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "show username",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.Author != "taylor-name" {
		t.Fatalf("CreateChangeset author = %q, want taylor-name", cs.Author)
	}

	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("hello\n"),
		Slice: &corev1.SliceRef{Account: "taylor-name", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/taylor-name/hello.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if patchset.Author != "taylor-name" {
		t.Fatalf("UpdateChangeset patchset author = %q, want taylor-name", patchset.Author)
	}

	got, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Author != "taylor-name" {
		t.Fatalf("GetChangeset author = %q, want taylor-name", got.Author)
	}
	if len(got.Patchsets) != 1 || got.Patchsets[0].Author != "taylor-name" {
		t.Fatalf("GetChangeset patchset authors = %#v, want taylor-name", got.Patchsets)
	}

	listed, err := handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "taylor-name", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Changesets) != 1 || listed.Changesets[0].Author != "taylor-name" {
		t.Fatalf("ListChangesets authors = %#v, want taylor-name", listed.Changesets)
	}
	if len(listed.Changesets[0].Patchsets) != 1 || listed.Changesets[0].Patchsets[0].Author != "taylor-name" {
		t.Fatalf("ListChangesets patchset authors = %#v, want taylor-name", listed.Changesets[0].Patchsets)
	}

	unresolvedCtx := authctx.WithSubjectID(context.Background(), "user_alice")
	unresolved, err := handlers.Changeset.CreateChangeset(unresolvedCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "fallback author",
	})
	if err != nil {
		t.Fatal(err)
	}
	if unresolved.Author != "user_alice" {
		t.Fatalf("unresolved CreateChangeset author = %q, want user_alice", unresolved.Author)
	}
	unresolvedGot, err := handlers.Changeset.GetChangeset(unresolvedCtx, &corev1.GetChangesetRequest{ChangesetId: unresolved.Id})
	if err != nil {
		t.Fatal(err)
	}
	if unresolvedGot.Author != "user_alice" {
		t.Fatalf("unresolved GetChangeset author = %q, want user_alice", unresolvedGot.Author)
	}
}

func TestCreateChangesetRejectsNonDefaultTargetRef(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	_, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      "refs/global/branches/feature",
		Title:          "branch attempt",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateChangeset target ref error = %v, want InvalidArgument", err)
	}
}

func TestCreateChangesetDefaultsEmptyTargetRef(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:          "omitted target ref",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.TargetRef != storage.DefaultTargetRef {
		t.Fatalf("CreateChangeset target ref = %q, want %q", cs.TargetRef, storage.DefaultTargetRef)
	}
}

func TestChangesetUpdateRejectsMalformedFileEdits(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit *corev1.FileEdit
	}{
		{name: "empty delete path", edit: &corev1.FileEdit{Op: "delete"}},
		{name: "empty mkdir path", edit: &corev1.FileEdit{Op: "mkdir"}},
		{name: "rename missing old path", edit: &corev1.FileEdit{Op: "rename", Path: "/acme/new.txt"}},
		{name: "rename missing new path", edit: &corev1.FileEdit{Op: "rename", OldPath: "/acme/old.txt"}},
		{name: "rename same path", edit: &corev1.FileEdit{Op: "rename", OldPath: "/acme/same.txt", Path: "/acme/same.txt"}},
		{name: "old path on upsert", edit: &corev1.FileEdit{Op: "upsert", OldPath: "/acme/old.txt", Path: "/acme/new.txt", BlobId: "blob_missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
				AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
				TargetRef:      storage.DefaultTargetRef,
				BaseCommitId:   ref.CommitId,
				Title:          tc.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
				ChangesetId:  cs.Id,
				BaseCommitId: ref.CommitId,
				FileEdits:    []*corev1.FileEdit{tc.edit},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("UpdateChangeset error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestChangesetUpdateValidatesAndHydratesBlobContentHash(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("blob metadata\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "blob metadata",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/wrong-hash.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: "sha256:not-the-uploaded-bytes",
			Mode:        0o100644,
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UpdateChangeset wrong hash error = %v, want InvalidArgument", err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   "/acme/hydrated-hash.txt",
			BlobId: uploaded.BlobId,
			Mode:   0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := patchset.FileEdits[0].ContentHash; got != uploaded.ContentHash {
		t.Fatalf("patchset content hash = %q, want %q", got, uploaded.ContentHash)
	}
}
