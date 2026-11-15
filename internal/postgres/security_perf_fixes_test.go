package postgres

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestMigrationsTrackedOncePreservesBlameSnapshots(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	snapshot := storage.BlameSnapshot{
		CommitID:    "sha256:keep",
		Path:        "/acme/payment/keep.go",
		ContentHash: "sha256:keepcontent",
		Owners:      []string{"sha256:keep"},
	}
	if err := store.Blames().PutBlameSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	// A second Migrate (such as a server restart/deploy) must skip already-applied
	// migrations and must NOT re-run 0042_clear_blame_snapshots.sql.
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := store.Blames().BlameSnapshots(ctx, []string{"sha256:keep"})
	if err != nil {
		t.Fatal(err)
	}
	if got["sha256:keep"]["/acme/payment/keep.go"] == nil {
		t.Fatalf("blame snapshot wiped on second Migrate(): %+v", got)
	}

	var applied bool
	if err := store.db.QueryRowContext(ctx, `
		select exists(select 1 from schema_migrations where filename = '0052_security_perf_indexes.sql')
	`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("0052_security_perf_indexes.sql not recorded in schema_migrations")
	}
}

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

func TestAppendTriggerRunLogConcurrentLinearSeq(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	created, err := store.Triggers().CreateTriggerRuns(ctx, []storage.TriggerRun{{
		Account:     "acme",
		TriggerID:   "lint",
		Folder:      "/acme/payment",
		Action:      "webhook",
		EventID:     "evt_concurrent_logs",
		EventKind:   "changeset.published",
		TargetRef:   DefaultTargetRef,
		MaxAttempts: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created runs = %d, want 1", len(created))
	}
	runID := created[0].ID

	const workers = 16
	seqs := make([]int64, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			seqs[i], errs[i] = store.Triggers().AppendTriggerRunLog(ctx, runID, "stdout", fmt.Sprintf("line %d\n", i))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d AppendTriggerRunLog error: %v", i, err)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("sorted seqs = %v, want 1..%d", seqs, workers)
		}
	}

	logs, err := store.Triggers().ListTriggerRunLogs(ctx, runID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != workers {
		t.Fatalf("stored logs = %d, want %d", len(logs), workers)
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
