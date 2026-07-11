package service

import (
	"context"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestChangesetUpdateAcceptsBlobUploadedThroughAuthoringSlice(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	authoringRef := &corev1.SliceRef{Account: "acme", Slice: "home"}
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Slice: authoringRef,
		Data:  []byte("authoring slice content\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	authoringSlice, err := mem.Slices.Resolve(ctx, authoringRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireEditBlobsAccessible(ctx, mem.Blobs, authoringSlice, []*corev1.FileEdit{{
		Path:        "/acme/content-hash-only.txt",
		ContentHash: uploaded.ContentHash,
	}}); err != nil {
		t.Fatalf("requireEditBlobsAccessible with authoring-slice content hash: %v", err)
	}
	cs, baseCommitID := createBlobAccessChangeset(t, ctx, handlers, authoringRef)

	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/available.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
		}},
	})
	if err != nil {
		t.Fatalf("UpdateChangeset with authoring-slice blob: %v", err)
	}
}

func TestChangesetUpdateRejectsBlobAssociatedOnlyWithUnrelatedSlice(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	authoringRef := &corev1.SliceRef{Account: "acme", Slice: "home"}
	unrelatedRef := &corev1.SliceRef{Account: "acme", Slice: "unrelated"}
	mem.PutSlice(unrelatedRef, []string{"/acme/unrelated"}, "private")
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Slice: unrelatedRef,
		Data:  []byte("unrelated slice content\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, baseCommitID := createBlobAccessChangeset(t, ctx, handlers, authoringRef)
	path := "/acme/claimed-by-hash.txt"

	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        path,
			ContentHash: uploaded.ContentHash,
		}},
	})
	assertEditBlobUnavailable(t, err, path)

	stored, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Patchsets) != 0 {
		t.Fatalf("inaccessible edit persisted %d patchsets, want none", len(stored.Patchsets))
	}
}

func TestChangesetUpdateAcceptsBlobCoveredByAuthoringSlicePathHead(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	authoringRef := &corev1.SliceRef{Account: "acme", Slice: "authoring"}
	unrelatedRef := &corev1.SliceRef{Account: "acme", Slice: "unrelated"}
	mem.PutSlice(authoringRef, []string{"/acme/authoring"}, "private")
	mem.PutSlice(unrelatedRef, []string{"/acme/unrelated"}, "private")
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Slice: unrelatedRef,
		Data:  []byte("path-head-covered content\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	mem.PutCommitWithFiles("commit_blob_covered_by_authoring_slice", []storage.FileEntry{{
		Path:        "/acme/authoring/existing.txt",
		BlobID:      uploaded.BlobId,
		ContentHash: uploaded.ContentHash,
		Mode:        0o100644,
		Size:        uploaded.Size,
	}}, []string{"/acme/authoring/existing.txt"})
	cs, baseCommitID := createBlobAccessChangeset(t, ctx, handlers, authoringRef)

	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/authoring/copied.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
		}},
	})
	if err != nil {
		t.Fatalf("UpdateChangeset with path-head-covered blob: %v", err)
	}
}

func TestChangesetUpdateWithoutBlobReferenceIsUnaffected(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	authoringRef := &corev1.SliceRef{Account: "acme", Slice: "home"}
	cs, baseCommitID := createBlobAccessChangeset(t, ctx, handlers, authoringRef)

	_, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:   "mkdir",
			Path: "/acme/reference-free-directory",
		}},
	})
	if err != nil {
		t.Fatalf("UpdateChangeset without blob reference: %v", err)
	}
}

func TestChangesetUpdateRejectsBlobIDResolvingToInaccessibleHash(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	authoringRef := &corev1.SliceRef{Account: "acme", Slice: "home"}
	unrelatedRef := &corev1.SliceRef{Account: "acme", Slice: "unrelated"}
	mem.PutSlice(unrelatedRef, []string{"/acme/unrelated"}, "private")
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Slice: unrelatedRef,
		Data:  []byte("inaccessible blob id content\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, baseCommitID := createBlobAccessChangeset(t, ctx, handlers, authoringRef)
	path := "/acme/claimed-by-blob-id.txt"

	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   path,
			BlobId: uploaded.BlobId,
		}},
	})
	assertEditBlobUnavailable(t, err, path)

	missingCS, missingBaseCommitID := createBlobAccessChangeset(t, ctx, handlers, authoringRef)
	missingPath := "/acme/missing-blob-id.txt"
	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  missingCS.Id,
		BaseCommitId: missingBaseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   missingPath,
			BlobId: "blob_does_not_exist",
		}},
	})
	assertEditBlobUnavailable(t, err, missingPath)
}

func createBlobAccessChangeset(t *testing.T, ctx context.Context, handlers *Handlers, authoringRef *corev1.SliceRef) (*corev1.Changeset, string) {
	t.Helper()
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: authoringRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "blob access gate",
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs, ref.CommitId
}

func assertEditBlobUnavailable(t *testing.T, err error, path string) {
	t.Helper()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("UpdateChangeset error = %v, want FailedPrecondition", err)
	}
	want := "content for " + path + " is not available to this slice; upload the blob first"
	if !strings.Contains(status.Convert(err).Message(), want) {
		t.Fatalf("UpdateChangeset error = %q, want %q", status.Convert(err).Message(), want)
	}
}
