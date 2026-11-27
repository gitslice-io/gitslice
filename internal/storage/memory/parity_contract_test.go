package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestStoreParitySliceValidationRejectsCommas(t *testing.T) {
	stores := New()
	_, err := stores.Slices.ValidateSliceDefinition(
		&corev1.SliceRef{Account: "acme", Slice: "app"},
		&corev1.SliceDefinition{IncludedPaths: []string{"/acme/a,/acme/b"}},
	)
	if !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for comma in included path, got %v", err)
	}
}

func TestStoreParityListSortedKeysetAndLimitCap(t *testing.T) {
	ctx := context.Background()
	stores := New()
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	for i, slug := range []string{"home", "alpha", "beta", "gamma"} {
		included := []string{"/acme/" + slug}
		if slug == "home" {
			included = []string{"/acme"}
		}
		sl, err := stores.Slices.CreateDefinition(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: slug}, &corev1.SliceDefinition{
			IncludedPaths: included,
			Visibility:    "public",
		})
		if err != nil {
			t.Fatalf("create slice %s: %v", slug, err)
		}
		stores.backend.mu.Lock()
		stores.backend.slices[sl.Id].CreatedAt = base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		stores.backend.slices[sl.Id].LastActivityAt = base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		stores.backend.mu.Unlock()
	}

	// Page 1 of newest-first sort: home pinned first, then gamma.
	page1, next1, err := stores.Slices.ListSorted(ctx, "acme", "public", "", "", 2, false, storage.SliceSortNewest)
	if err != nil {
		t.Fatalf("ListSorted page1: %v", err)
	}
	if len(page1) != 2 || page1[0].Ref.Slice != "home" || page1[1].Ref.Slice != "gamma" {
		t.Fatalf("unexpected page1: %+v", page1)
	}
	if next1 == "" {
		t.Fatalf("expected non-empty v1 keyset cursor")
	}
	if _, ok, err := storage.DecodeSliceCursor(next1); err != nil || !ok {
		t.Fatalf("expected canonical v1 slice cursor, got %q (err=%v)", next1, err)
	}

	// Page 2 using v1 keyset cursor: beta, alpha.
	page2, next2, err := stores.Slices.ListSorted(ctx, "acme", "public", "", next1, 2, false, storage.SliceSortNewest)
	if err != nil {
		t.Fatalf("ListSorted page2: %v", err)
	}
	if len(page2) != 2 || page2[0].Ref.Slice != "beta" || page2[1].Ref.Slice != "alpha" || next2 != "" {
		t.Fatalf("unexpected page2: len=%d next=%q", len(page2), next2)
	}

	// Invalid cursor must return ErrInvalid.
	if _, _, err := stores.Slices.ListSorted(ctx, "acme", "public", "", "bad-cursor", 2, false, storage.SliceSortNewest); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slice cursor, got %v", err)
	}
}

func TestStoreParityListCommitPageHonorsRefAndPagination(t *testing.T) {
	ctx := context.Background()
	stores := New()
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	stores.backend.mu.Lock()
	stores.backend.commits["c1"] = &corev1.Commit{Id: "c1", CreatedAt: base.Add(1 * time.Minute).Format(time.RFC3339Nano), ChangedPaths: []string{"/acme/app/a.go"}}
	stores.backend.commits["c2"] = &corev1.Commit{Id: "c2", ParentIds: []string{"c1"}, CreatedAt: base.Add(2 * time.Minute).Format(time.RFC3339Nano), ChangedPaths: []string{"/acme/app/b.go"}}
	stores.backend.commits["c3"] = &corev1.Commit{Id: "c3", ParentIds: []string{"c2"}, CreatedAt: base.Add(3 * time.Minute).Format(time.RFC3339Nano), ChangedPaths: []string{"/acme/app/c.go"}}
	stores.backend.refs[storage.DefaultTargetRef] = &corev1.Ref{Name: storage.DefaultTargetRef, CommitId: "c3"}
	stores.backend.mu.Unlock()

	if _, err := stores.Repository.ListCommitPage(ctx, "refs/global/does-not-exist", 10, ""); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown ref, got %v", err)
	}
	if _, err := stores.Repository.ListCommitPage(ctx, storage.DefaultTargetRef, 10, "not-base64-json"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for malformed page token, got %v", err)
	}

	p1, err := stores.Repository.ListCommitPage(ctx, storage.DefaultTargetRef, 2, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(p1.Commits) != 2 || p1.Commits[0].Id != "c3" || p1.Commits[1].Id != "c2" || p1.NextPageToken == "" {
		t.Fatalf("unexpected page1: commits=%v next=%q", p1.Commits, p1.NextPageToken)
	}

	p2, err := stores.Repository.ListCommitPage(ctx, storage.DefaultTargetRef, 2, p1.NextPageToken)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(p2.Commits) != 1 || p2.Commits[0].Id != "c1" || p2.NextPageToken != "" {
		t.Fatalf("unexpected page2: commits=%v next=%q", p2.Commits, p2.NextPageToken)
	}
}
