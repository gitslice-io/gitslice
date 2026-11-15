package postgres

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestListCommitPageRecursiveCTEPagination(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	wantOrder, err := store.Repository().CommitAncestry(ctx, base.CommitId, 50)
	if err != nil {
		t.Fatal(err)
	}

	headID := base.CommitId
	for i := 0; i < 4; i++ {
		blobID, contentHash := upsertTestBlob(t, ctx, store, fmt.Sprintf("commit %d\n", i))
		headID = publishRepositoryTestEdits(t, ctx, store, headID, []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        fmt.Sprintf("/acme/payment/page_%d.txt", i),
			BlobId:      blobID,
			ContentHash: contentHash,
			Mode:        0o100644,
		}})
		wantOrder = append([]string{headID}, wantOrder...)
	}

	var gotIDs []string
	pageToken := ""
	for {
		page, err := store.Repository().ListCommitPage(ctx, DefaultTargetRef, 2, pageToken)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Commits {
			gotIDs = append(gotIDs, c.Id)
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	if !reflect.DeepEqual(gotIDs, wantOrder) {
		t.Fatalf("paged commits = %v, want %v", gotIDs, wantOrder)
	}

	bogusToken, err := encodeCommitPageToken("sha256:not_in_history", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Repository().ListCommitPage(ctx, DefaultTargetRef, 2, bogusToken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ListCommitPage(bogus cursor) err = %v, want ErrInvalid", err)
	}
}

func TestRecordGitImportCommitIncrementsCountIdempotently(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	imp, err := store.Repository().GetOrCreateGitImport(
		ctx,
		"user_alice",
		"https://example.com/repo.git",
		"/acme/payment",
		sliceRef,
		"slice_acme_payment",
		DefaultTargetRef,
		"history",
		2,
	)
	if err != nil {
		t.Fatal(err)
	}

	first := GitImportedCommitRecord{
		ImportID:         imp.ID,
		GitCommitID:      "1111111111111111111111111111111111111111",
		NativeCommitID:   "sha256:native1",
		Message:          "first",
		Position:         1,
		ChangedPathCount: 2,
		AuthorName:       "Alice",
		AuthorEmail:      "alice@example.com",
		AuthoredAt:       "2026-01-01T00:00:00Z",
		FullMessage:      "first\n",
	}
	second := GitImportedCommitRecord{
		ImportID:         imp.ID,
		GitCommitID:      "2222222222222222222222222222222222222222",
		NativeCommitID:   "sha256:native2",
		Message:          "second",
		Position:         2,
		ChangedPathCount: 1,
		AuthorName:       "Alice",
		AuthorEmail:      "alice@example.com",
		AuthoredAt:       "2026-01-01T00:01:00Z",
		FullMessage:      "second\n",
	}
	if err := store.Repository().RecordGitImportCommit(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Repository().RecordGitImportCommit(ctx, second); err != nil {
		t.Fatal(err)
	}
	// Re-recording an already-imported git commit (resume/retry) must not double-count.
	if err := store.Repository().RecordGitImportCommit(ctx, first); err != nil {
		t.Fatal(err)
	}

	got, err := store.Repository().GetGitImport(ctx, "https://example.com/repo.git", "/acme/payment", "slice_acme_payment", DefaultTargetRef, "history")
	if err != nil {
		t.Fatal(err)
	}
	if got.ImportedCount != 2 {
		t.Fatalf("ImportedCount = %d, want 2 after duplicate commit retry", got.ImportedCount)
	}
}
