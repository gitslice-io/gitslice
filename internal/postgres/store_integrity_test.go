package postgres

import (
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
)

func TestStorageIntegrityVerifierPassesAfterPublish(t *testing.T) {
	ctx, store, objectStore := newPostgresTestStoreWithObjects(t)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst Integrity = true\n")

	patchset := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/integrity.go", blobID, contentHash)
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	report, err := store.VerifyIntegrity(ctx, objectStore)
	if err != nil {
		t.Fatalf("VerifyIntegrity failed: %v\nfindings: %#v", err, report.Findings)
	}
	if !report.OK() {
		t.Fatalf("integrity report not OK: %#v", report)
	}
	if report.RefCount == 0 || report.CommitCount == 0 || report.BlobCount == 0 || report.TreeCount == 0 {
		t.Fatalf("integrity report did not inspect expected state: %#v", report)
	}
}

func TestStorageIntegrityVerifierDetectsMissingBlobObject(t *testing.T) {
	ctx, store, objectStore := newPostgresTestStoreWithObjects(t)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst Broken = true\n")

	patchset := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/broken.go", blobID, contentHash)
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := objectStore.Delete(ctx, filesystem.BlobKey(contentHash)); err != nil {
		t.Fatal(err)
	}

	report, err := store.VerifyIntegrity(ctx, objectStore)
	var integrityErr IntegrityError
	if !errors.As(err, &integrityErr) {
		t.Fatalf("VerifyIntegrity err = %v, want IntegrityError\nreport: %#v", err, report)
	}
	if !hasFinding(report, "blob_object_unreadable") {
		t.Fatalf("expected blob_object_unreadable finding, got %#v", report.Findings)
	}
}
