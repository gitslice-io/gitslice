package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestSliceDefinitionValidation(t *testing.T) {
	ref := &corev1.SliceRef{Account: "nic", Slice: "tools"}
	included, visibility, err := validateSliceDefinition(ref, []string{"/nic/tools", "nic/tools"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if visibility != "account" {
		t.Fatalf("visibility = %q, want account", visibility)
	}
	if len(included) != 1 || included[0] != "/nic/tools" {
		t.Fatalf("included = %#v, want [/nic/tools]", included)
	}
	if _, _, err := validateSliceDefinition(ref, []string{"/other/tools"}, "account"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outside account err = %v, want ErrInvalid", err)
	}
	if _, _, err := validateSliceDefinition(ref, []string{"/nic"}, "account"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("custom account-root err = %v, want ErrInvalid", err)
	}
	homeIncluded, _, err := validateSliceDefinition(&corev1.SliceRef{Account: "nic", Slice: "home"}, []string{"/nic"}, "account")
	if err != nil {
		t.Fatal(err)
	}
	if len(homeIncluded) != 1 || homeIncluded[0] != "/nic" {
		t.Fatalf("home included = %#v, want [/nic]", homeIncluded)
	}
}

func TestStoragePublishesObjectStoreTreeAndReadsFiles(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	path := "/acme/payment/a.go"

	patchset := createDraftPatchset(t, ctx, store, base.CommitId, path, blobID, contentHash)
	submit, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id)
	if err != nil {
		t.Fatal(err)
	}
	if submit.Status != "pending_publish" {
		t.Fatalf("submit status = %q, want pending_publish", submit.Status)
	}
	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	ref := getTestRef(t, ctx, store)
	if ref.CommitId == base.CommitId {
		t.Fatal("ref did not move after publish")
	}
	commit, err := store.Repository().GetCommit(ctx, ref.CommitId)
	if err != nil {
		t.Fatal(err)
	}
	if commit.RootTreeId == "" {
		t.Fatal("published commit has empty root_tree_id")
	}
	entry, err := store.Repository().GetFile(ctx, ref.CommitId, path)
	if err != nil {
		t.Fatal(err)
	}
	if entry.BlobID != blobID || entry.ContentHash != contentHash {
		t.Fatalf("entry = %#v, want blob=%s hash=%s", entry, blobID, contentHash)
	}
	files, err := store.Repository().ListFiles(ctx, ref.CommitId, "/acme/payment")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != path {
		t.Fatalf("files = %#v, want only %s", files, path)
	}
}

func TestStoragePathHeadRejectsSamePathBeforePublish(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	path := "/acme/payment/conflict.go"
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst V = 1\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst V = 2\n")

	first := createDraftPatchset(t, ctx, store, base.CommitId, path, firstBlobID, firstHash)
	second := createDraftPatchset(t, ctx, store, base.CommitId, path, secondBlobID, secondHash)

	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); !errors.Is(err, ErrConflict) {
		t.Fatalf("second submit err = %v, want ErrConflict", err)
	}
	ref := getTestRef(t, ctx, store)
	if ref.CommitId != base.CommitId {
		t.Fatal("ref moved before pending publish was processed")
	}
}

func TestStorageDisjointPendingPublishesAsBatch(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst B = 1\n")

	first := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/a.go", firstBlobID, firstHash)
	second := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/b.go", secondBlobID, secondHash)
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); err != nil {
		t.Fatal(err)
	}
	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 2 {
		t.Fatalf("published = %d, want 2", published)
	}
	ref := getTestRef(t, ctx, store)
	for _, path := range []string{"/acme/payment/a.go", "/acme/payment/b.go"} {
		if _, err := store.Repository().GetFile(ctx, ref.CommitId, path); err != nil {
			t.Fatalf("GetFile(%s): %v", path, err)
		}
	}
}

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

func newPostgresTestStore(t *testing.T) (context.Context, *DB) {
	t.Helper()
	ctx, store, _ := newPostgresTestStoreWithObjects(t)
	return ctx, store
}

func newPostgresTestStoreWithObjects(t *testing.T) (context.Context, *DB, *filesystem.Store) {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run Postgres storage tests")
	}
	ctx := context.Background()
	schema := "gitslice_store_" + sanitizeSchemaName(t.Name()) + "_" + time.Now().Format("150405000000")
	createTestSchema(t, databaseURL, schema)
	store, err := Open(ctx, databaseURLWithSearchPath(t, databaseURL, schema))
	if err != nil {
		t.Fatal(err)
	}
	objectStore, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.SetTreeStore(treestore.New(objectStore))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		dropTestSchema(t, databaseURL, schema)
	})
	return ctx, store, objectStore
}

func createDraftPatchset(t *testing.T, ctx context.Context, store *DB, baseCommitID, path, blobID, contentHash string) *corev1.Patchset {
	t.Helper()
	cs, err := store.Changesets().Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   baseCommitID,
		Title:          "storage test",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := store.Changesets().AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: baseCommitID,
		Author:       "user_alice",
		ChangedPaths: []string{path},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        path,
			BlobId:      blobID,
			ContentHash: contentHash,
			Mode:        0o100644,
		}},
		PathBases: []*corev1.PathBase{{
			Path:             path,
			BaseCommitId:     baseCommitID,
			Exists:           false,
			EntryFingerprint: MissingEntryFingerprint(),
			Check:            "entry_fingerprint",
		}},
		ReadSet:  []*corev1.PathSetEntry{{Path: path}},
		WriteSet: []*corev1.PathSetEntry{{Path: path}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return patchset
}

func upsertTestBlob(t *testing.T, ctx context.Context, store *DB, content string) (string, string) {
	t.Helper()
	data := []byte(content)
	blobID := objectid.BlobID(data)
	contentHash := objectid.RawContentHash(data)
	if err := store.Blobs().Upsert(ctx, blobID, contentHash, int64(len(data)), filesystem.BlobKey(contentHash)); err != nil {
		t.Fatal(err)
	}
	return blobID, contentHash
}

func upsertTestBlobObject(t *testing.T, ctx context.Context, store *DB, objectStore *filesystem.Store, content string) (string, string) {
	t.Helper()
	data := []byte(content)
	blobID := objectid.BlobID(data)
	contentHash := objectid.RawContentHash(data)
	key := filesystem.BlobKey(contentHash)
	if err := objectStore.Put(ctx, key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Blobs().Upsert(ctx, blobID, contentHash, int64(len(data)), key); err != nil {
		t.Fatal(err)
	}
	return blobID, contentHash
}

func hasFinding(report IntegrityReport, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func getTestRef(t *testing.T, ctx context.Context, store *DB) *corev1.Ref {
	t.Helper()
	ref, err := store.Repository().GetRef(ctx, DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func createTestSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create schema ` + schema); err != nil {
		t.Fatal(err)
	}
}

func dropTestSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`drop schema if exists ` + schema + ` cascade`); err != nil {
		t.Fatal(err)
	}
}

func databaseURLWithSearchPath(t *testing.T, rawURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func sanitizeSchemaName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "test"
	}
	return fmt.Sprintf("%.48s", b.String())
}
