package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClientDrivenGitImportBatchAndResume(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "home"}

	upload := func(data string) (string, string) {
		t.Helper()
		b := []byte(data)
		res, err := handlers.Blob.UploadBlobs(ctx, &corev1.UploadBlobsRequest{
			Slice: sliceRef,
			Blobs: []*corev1.UploadBlobsEntry{{
				ContentHash: objectid.RawContentHash(b),
				Data:        b,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.Blobs[0].BlobId, res.Blobs[0].ContentHash
	}

	blobA1, hashA1 := upload("v1\n")
	blobA2, hashA2 := upload("v2\n")
	blobB1, hashB1 := upload("hello b\n")

	begin, err := handlers.Repository.BeginGitImport(ctx, &corev1.BeginGitImportRequest{
		Source:         "/local/repo",
		MountPath:      "/acme/local-repo",
		AuthoringSlice: sliceRef,
		Mode:           "deep",
		TotalCommits:   3,
		Resume:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if begin.ImportId == "" {
		t.Fatal("expected non-empty import_id")
	}

	batch1Req := &corev1.ImportGitCommitBatchRequest{
		ImportId:       begin.ImportId,
		AuthoringSlice: sliceRef,
		MountPath:      "/acme/local-repo",
		Commits: []*corev1.ImportGitCommitInput{
			{
				GitCommitId: "1111111111111111111111111111111111111111",
				Position:    1,
				Message:     "initial commit",
				FullMessage: "initial commit",
				AuthorName:  "Alice",
				AuthorEmail: "alice@example.invalid",
				AuthoredAt:  "2026-01-01T00:00:00Z",
				Edits: []*corev1.FileEdit{
					{Op: "upsert", Path: "/acme/local-repo/a.txt", BlobId: blobA1, ContentHash: hashA1, Mode: 0o100644},
					{Op: "upsert", Path: "/acme/local-repo/b.txt", BlobId: blobB1, ContentHash: hashB1, Mode: 0o100644},
				},
			},
			{
				GitCommitId: "2222222222222222222222222222222222222222",
				Position:    2,
				Message:     "update a and delete b",
				FullMessage: "update a and delete b\n\nDetailed body",
				Description: "Detailed body",
				AuthorName:  "Bob",
				AuthorEmail: "bob@example.invalid",
				AuthoredAt:  "2026-01-02T00:00:00Z",
				Edits: []*corev1.FileEdit{
					{Op: "upsert", Path: "/acme/local-repo/a.txt", BlobId: blobA2, ContentHash: hashA2, Mode: 0o100644},
					{Op: "delete", Path: "/acme/local-repo/b.txt"},
				},
			},
		},
	}
	res1, err := handlers.Repository.ImportGitCommitBatch(ctx, batch1Req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res1.Commits) != 2 || res1.FinalCommitId == "" {
		t.Fatalf("unexpected batch1 result: %+v", res1)
	}

	// Idempotent retry of the same batch returns the exact same native commits.
	res1Retry, err := handlers.Repository.ImportGitCommitBatch(ctx, batch1Req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res1Retry.Commits) != 2 || res1Retry.Commits[0].NativeCommitId != res1.Commits[0].NativeCommitId || res1Retry.FinalCommitId != res1.FinalCommitId {
		t.Fatalf("idempotent retry mismatch: got %+v, want %+v", res1Retry, res1)
	}

	// Resuming sees the 2 completed commits and the current mounted files.
	resumed, err := handlers.Repository.BeginGitImport(ctx, &corev1.BeginGitImportRequest{
		Source:         "/local/repo",
		MountPath:      "/acme/local-repo",
		AuthoringSlice: sliceRef,
		Mode:           "deep",
		TotalCommits:   3,
		Resume:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ImportId != begin.ImportId || len(resumed.CompletedCommits) != 2 {
		t.Fatalf("resumed = %+v, want import_id=%s and 2 completed commits", resumed, begin.ImportId)
	}
	if len(resumed.MountedFiles) != 1 || resumed.MountedFiles[0].Path != "/acme/local-repo/a.txt" {
		t.Fatalf("mounted_files = %+v, want only a.txt", resumed.MountedFiles)
	}

	// Complete import and verify file contents + GitImportInfo metadata.
	if _, err := handlers.Repository.CompleteGitImport(ctx, &corev1.CompleteGitImportRequest{
		ImportId:       begin.ImportId,
		AuthoringSlice: sliceRef,
		MountPath:      "/acme/local-repo",
		FinalCommitId:  res1.FinalCommitId,
	}); err != nil {
		t.Fatal(err)
	}

	gotA, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: res1.FinalCommitId,
		Path:     "/acme/local-repo/a.txt",
	})
	if err != nil || string(gotA.Data) != "v2\n" {
		t.Fatalf("ReadFile a.txt = (%q, %v), want v2", string(gotA.GetData()), err)
	}
	if _, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: res1.FinalCommitId,
		Path:     "/acme/local-repo/b.txt",
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("ReadFile deleted b.txt = %v, want NotFound", err)
	}

	commitMeta, err := handlers.Repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: res1.FinalCommitId})
	if err != nil {
		t.Fatal(err)
	}
	if commitMeta.GetGitImport().GetAuthorName() != "Bob" || commitMeta.GetGitImport().GetGitCommitId() != "2222222222222222222222222222222222222222" {
		t.Fatalf("unexpected git_import metadata: %+v", commitMeta.GetGitImport())
	}
}

func TestImportGitCommitBatchEnforcesMountBoundsAndRequirements(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "home"}

	b := []byte("x\n")
	up, err := handlers.Blob.UploadBlobs(ctx, &corev1.UploadBlobsRequest{
		Slice: sliceRef,
		Blobs: []*corev1.UploadBlobsEntry{{ContentHash: objectid.RawContentHash(b), Data: b}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Path outside mount_path is rejected.
	_, err = handlers.Repository.ImportGitCommitBatch(ctx, &corev1.ImportGitCommitBatchRequest{
		AuthoringSlice: sliceRef,
		MountPath:      "/acme/local-repo",
		Commits: []*corev1.ImportGitCommitInput{{
			GitCommitId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Position:    1,
			Message:     "escape mount",
			Edits: []*corev1.FileEdit{{
				Op:          "upsert",
				Path:        "/acme/other/file.txt",
				BlobId:      up.Blobs[0].BlobId,
				ContentHash: up.Blobs[0].ContentHash,
				Mode:        0o100644,
			}},
		}},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("edit outside mount_path = %v, want FailedPrecondition", err)
	}
}
