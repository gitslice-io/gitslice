package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sort"
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
	included, visibility, requiredApprovals, requiredChecks, err := validateSliceDefinition(ref, []string{"/nic/tools", "nic/tools"}, "", 1, []string{"unit", "unit"})
	if err != nil {
		t.Fatal(err)
	}
	if visibility != "private" {
		t.Fatalf("visibility = %q, want private", visibility)
	}
	if len(included) != 1 || included[0] != "/nic/tools" {
		t.Fatalf("included = %#v, want [/nic/tools]", included)
	}
	if requiredApprovals != 1 || len(requiredChecks) != 1 || requiredChecks[0] != "unit" {
		t.Fatalf("submit settings = %d %#v, want 1 [unit]", requiredApprovals, requiredChecks)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic/tools"}, "account", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("account visibility err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/other/tools"}, "private", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outside account err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic"}, "private", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("custom account-root err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic/tools,/nic/docs"}, "private", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("comma-separated include err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic/tools"}, "private", -1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative approvals err = %v, want ErrInvalid", err)
	}
	homeIncluded, _, _, _, err := validateSliceDefinition(&corev1.SliceRef{Account: "nic", Slice: "home"}, []string{"/nic"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(homeIncluded) != 1 || homeIncluded[0] != "/nic" {
		t.Fatalf("home included = %#v, want [/nic]", homeIncluded)
	}
}

func TestBlobStoreSliceAssociationsAndPathsByContentHash(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	_, firstHash := upsertTestBlob(t, ctx, store, "first blob\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "second blob\n")

	if err := store.Blobs().AssociateSlices(ctx, "slice_acme_payment", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Blobs().AssociateSlices(ctx, "slice_acme_payment", []string{firstHash, firstHash}); err != nil {
		t.Fatal(err)
	}
	if err := store.Blobs().AssociateSlices(ctx, "slice_acme_payment", []string{firstHash}); err != nil {
		t.Fatal(err)
	}

	associations, err := store.Blobs().SliceAssociations(ctx, "slice_acme_payment", []string{firstHash, secondHash})
	if err != nil {
		t.Fatal(err)
	}
	if !associations[firstHash] || associations[secondHash] {
		t.Fatalf("payment associations = %#v, want only %s", associations, firstHash)
	}
	otherAssociations, err := store.Blobs().SliceAssociations(ctx, "slice_acme_backend", []string{firstHash})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherAssociations) != 0 {
		t.Fatalf("backend associations = %#v, want none", otherAssociations)
	}
	emptyAssociations, err := store.Blobs().SliceAssociations(ctx, "slice_acme_payment", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyAssociations) != 0 {
		t.Fatalf("empty associations = %#v, want none", emptyAssociations)
	}

	if _, err := store.db.ExecContext(ctx, `
		insert into path_heads(path, exists, entry_fingerprint, blob_id, content_hash, mode, size, updated_at)
		values
			('/acme/backend/a.txt', true, 'fingerprint-a', $1, $2, 33188, 12, now()),
			('/acme/backend/b.txt', true, 'fingerprint-b', $1, $2, 33188, 12, now())
	`, secondBlobID, secondHash); err != nil {
		t.Fatal(err)
	}
	pathsByHash, err := store.Blobs().PathsByContentHash(ctx, []string{firstHash, secondHash, "sha256:missing"})
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"/acme/backend/a.txt", "/acme/backend/b.txt"}
	if !reflect.DeepEqual(pathsByHash[secondHash], wantPaths) || len(pathsByHash) != 1 {
		t.Fatalf("paths by hash = %#v, want %s at %#v", pathsByHash, secondHash, wantPaths)
	}
	emptyPaths, err := store.Blobs().PathsByContentHash(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyPaths) != 0 {
		t.Fatalf("empty paths = %#v, want none", emptyPaths)
	}
}

func TestAuthStoreCLILoginDeviceFlow(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	code, expiresAt, err := store.Auth().StartCliLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" || !expiresAt.After(time.Now().UTC()) {
		t.Fatalf("StartCliLogin = (%q, %s), want code and future expiry", code, expiresAt)
	}

	status, token, subjectID, err := store.Auth().PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if status != "pending" || token != "" || subjectID != "" {
		t.Fatalf("pending PollCliLogin = (%q, %q, %q), want pending without token", status, token, subjectID)
	}

	if err := store.Auth().CompleteCliLogin(ctx, code, "user_alice"); err != nil {
		t.Fatal(err)
	}

	status, token, subjectID, err = store.Auth().PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if status != "approved" || token == "" || subjectID != "user_alice" {
		t.Fatalf("approved PollCliLogin = (%q, %q, %q), want approved token for user_alice", status, token, subjectID)
	}
	subject, err := store.Auth().SubjectForToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if subject.ID != "user_alice" {
		t.Fatalf("SubjectForToken = %q, want user_alice", subject.ID)
	}

	var sessionExpiresAt time.Time
	if err := store.db.QueryRowContext(ctx, `
		select expires_at
		from sessions
		where token_hash = $1
	`, tokenHash(token)).Scan(&sessionExpiresAt); err != nil {
		t.Fatal(err)
	}
	sessionTTL := time.Until(sessionExpiresAt)
	if sessionTTL < 29*24*time.Hour || sessionTTL > 31*24*time.Hour {
		t.Fatalf("CLI session TTL = %s, want about 30 days", sessionTTL)
	}

	status, token, subjectID, err = store.Auth().PollCliLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if status != "expired" || token != "" || subjectID != "" {
		t.Fatalf("second PollCliLogin = (%q, %q, %q), want expired without token", status, token, subjectID)
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

func TestStoragePublishEnqueuesOutboxAndDrainIndexesChangedPaths(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlob(t, ctx, store, "package payment\nconst Indexed = true\n")
	path := "/acme/payment/indexed.go"

	patchset := createDraftPatchset(t, ctx, store, base.CommitId, path, blobID, contentHash)
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	commitID := getTestRef(t, ctx, store).CommitId
	if depth := outboxDepthForTest(t, ctx, store); depth != 1 {
		t.Fatalf("outbox depth = %d, want 1", depth)
	}
	if count := commitChangedPathCountForTest(t, ctx, store, commitID, path); count != 0 {
		t.Fatalf("commit_changed_paths before drain = %d, want 0", count)
	}

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.Changesets().WaitForOutboxDrain(drainCtx); err != nil {
		t.Fatal(err)
	}
	if depth := outboxDepthForTest(t, ctx, store); depth != 0 {
		t.Fatalf("outbox depth after drain = %d, want 0", depth)
	}
	if count := commitChangedPathCountForTest(t, ctx, store, commitID, path); count != 1 {
		t.Fatalf("commit_changed_paths after drain = %d, want 1", count)
	}
}

func TestStorageOutboxFailureIncrementsAttemptsAndRetries(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	var id int64
	if err := store.db.QueryRowContext(ctx, `
		insert into outbox(kind, payload)
		values ('unknown_kind', '{}'::jsonb)
		returning id
	`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	result, err := store.Changesets().ProcessOutbox(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 0 || result.Failed != 1 {
		t.Fatalf("ProcessOutbox result = %#v, want 0 processed and 1 failed", result)
	}
	if attempts := outboxAttemptsForTest(t, ctx, store, id); attempts != 1 {
		t.Fatalf("attempts after first failure = %d, want 1", attempts)
	}

	result, err = store.Changesets().ProcessOutbox(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 0 || result.Failed != 1 {
		t.Fatalf("retry ProcessOutbox result = %#v, want 0 processed and 1 failed", result)
	}
	if attempts := outboxAttemptsForTest(t, ctx, store, id); attempts != 2 {
		t.Fatalf("attempts after retry = %d, want 2", attempts)
	}
}

func TestStorageRebuildDerivedIndexesMatchesIncrementalChangedPaths(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	first := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/rebuild_a.go", firstBlobID, firstHash)
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("first published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst B = 1\n")
	second := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/rebuild_b.go", secondBlobID, secondHash)
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("second published = %d, want 1", published)
	}
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.Changesets().WaitForOutboxDrain(drainCtx); err != nil {
		t.Fatal(err)
	}
	incremental := commitChangedPathRowsForTest(t, ctx, store, DefaultTargetRef)
	if len(incremental) < 2 {
		t.Fatalf("incremental changed-path rows = %#v, want at least 2", incremental)
	}

	if err := store.Changesets().RebuildDerivedIndexes(ctx, DefaultTargetRef); err != nil {
		t.Fatal(err)
	}
	rebuilt := commitChangedPathRowsForTest(t, ctx, store, DefaultTargetRef)
	if !reflect.DeepEqual(rebuilt, incremental) {
		t.Fatalf("rebuilt changed-path rows differ\nincremental=%#v\nrebuilt=%#v", incremental, rebuilt)
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

func TestStorageRefreshesDirectoryPathHeadsAfterPublish(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst A = 1\n")
	first := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/src"},
		{Op: "upsert", Path: "/acme/payment/src/a.go", BlobId: firstBlobID, ContentHash: firstHash, Mode: 0o100644},
	})
	if _, err := store.Changesets().Submit(ctx, first.ChangesetId, first.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst B = 1\n")
	second := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/src"},
		{Op: "upsert", Path: "/acme/payment/src/b.go", BlobId: secondBlobID, ContentHash: secondHash, Mode: 0o100644},
	})
	if _, err := store.Changesets().Submit(ctx, second.ChangesetId, second.Id); err != nil {
		t.Fatalf("second submit should accept existing mkdir after publish: %v", err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	ref := getTestRef(t, ctx, store)
	for _, path := range []string{"/acme/payment/src/a.go", "/acme/payment/src/b.go"} {
		if _, err := store.Repository().GetFile(ctx, ref.CommitId, path); err != nil {
			t.Fatalf("GetFile(%s): %v", path, err)
		}
	}
}

func TestStorageRefreshesRenamedDirectoryDescendantPathHeads(t *testing.T) {
	ctx, store, objectStore := newPostgresTestStoreWithObjects(t)
	base := getTestRef(t, ctx, store)
	firstBlobID, firstHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst A = 1\n")
	secondBlobID, secondHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst B = 1\n")
	seed := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/src"},
		{Op: "mkdir", Path: "/acme/payment/src/nested"},
		{Op: "upsert", Path: "/acme/payment/src/a.go", BlobId: firstBlobID, ContentHash: firstHash, Mode: 0o100644},
		{Op: "upsert", Path: "/acme/payment/src/nested/b.go", BlobId: secondBlobID, ContentHash: secondHash, Mode: 0o100644},
	})
	if _, err := store.Changesets().Submit(ctx, seed.ChangesetId, seed.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	move := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:      "rename",
		OldPath: "/acme/payment/src",
		Path:    "/acme/payment/moved",
	}})
	if _, err := store.Changesets().Submit(ctx, move.ChangesetId, move.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	ref := getTestRef(t, ctx, store)
	if _, err := store.Repository().GetFile(ctx, ref.CommitId, "/acme/payment/moved/nested/b.go"); err != nil {
		t.Fatalf("moved file not readable: %v", err)
	}
	report, err := store.VerifyIntegrity(ctx, objectStore)
	if err != nil || !report.OK() {
		t.Fatalf("integrity after directory rename failed: err=%v report=%#v", err, report)
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
	seedStoreTestFixture(t, ctx, store)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		dropTestSchema(t, databaseURL, schema)
	})
	return ctx, store, objectStore
}

// seedStoreTestFixture provisions the acme account, dev subjects, and the
// payment/backend slices the lower-level storage tests operate against. Prod no
// longer seeds dev data (it is created at runtime via ChooseUsername), so the
// fixture lives here in test code.
func seedStoreTestFixture(t *testing.T, ctx context.Context, store *DB) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at)
		values
			('user_alice', 'user', 'Alice', now()),
			('user_bob', 'user', 'Bob', now()),
			('ci_bot', 'service_account', 'CI Bot', now())
		on conflict (id) do nothing
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into accounts(id, slug, kind, created_at, updated_at)
		values ('acct_acme', 'acme', 'org', now(), now())
		on conflict (id) do nothing
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values
			('acct_acme', 'user_alice', 'admin', now()),
			('acct_acme', 'user_bob', 'writer', now()),
			('acct_acme', 'ci_bot', 'writer', now())
		on conflict do nothing
	`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedStoreTestSlice(t, ctx, tx, "slice_acme_payment", "payment", []string{"/acme/payment"})
	seedStoreTestSlice(t, ctx, tx, "slice_acme_backend", "backend", []string{"/acme/backend", "/acme/payment/shared"})
	if err := ensureAccountRootDirectoryTx(ctx, tx, "acme", "system", store.repository.trees); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedStoreTestSlice(t *testing.T, ctx context.Context, tx *sql.Tx, id, slug string, included []string) {
	t.Helper()
	includedJSON, err := encodeJSON(included)
	if err != nil {
		t.Fatal(err)
	}
	emptyChecksJSON, err := encodeJSON([]string{})
	if err != nil {
		t.Fatal(err)
	}
	hash := definitionHash(id, 1, included, "private", 0, nil)
	if _, err := tx.ExecContext(ctx, `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, created_at, updated_at)
		values ($1, 'acct_acme', $2, 1, $3, 'private', $4, now(), now())
		on conflict (id) do nothing
	`, id, slug, hash, includedJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		insert into slice_definition_versions(
			slice_id, version, definition_hash, visibility, included_paths,
			required_approvals, required_checks, created_at, created_by
		)
		values ($1, 1, $2, 'private', $3, 0, $4, now(), 'system')
		on conflict do nothing
	`, id, hash, includedJSON, emptyChecksJSON); err != nil {
		t.Fatal(err)
	}
	if err := syncSliceIncludedPathsTx(ctx, tx, id, included); err != nil {
		t.Fatal(err)
	}
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

func createDraftPatchsetWithEdits(t *testing.T, ctx context.Context, store *DB, baseCommitID string, edits []*corev1.FileEdit) *corev1.Patchset {
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
	changedPaths := changedPathsForTestEdits(edits)
	pathBases := make([]*corev1.PathBase, 0, len(changedPaths))
	readSet := make([]*corev1.PathSetEntry, 0, len(changedPaths))
	writeSet := make([]*corev1.PathSetEntry, 0, len(changedPaths))
	for _, p := range changedPaths {
		pathBases = append(pathBases, pathBaseForTestPath(t, ctx, store, baseCommitID, p))
		readSet = append(readSet, &corev1.PathSetEntry{Path: p})
		writeSet = append(writeSet, &corev1.PathSetEntry{Path: p})
	}
	patchset, err := store.Changesets().AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: baseCommitID,
		Author:       "user_alice",
		ChangedPaths: changedPaths,
		FileEdits:    edits,
		PathBases:    pathBases,
		ReadSet:      readSet,
		WriteSet:     writeSet,
	})
	if err != nil {
		t.Fatal(err)
	}
	return patchset
}

func changedPathsForTestEdits(edits []*corev1.FileEdit) []string {
	seen := map[string]struct{}{}
	for _, edit := range edits {
		if edit.Path != "" {
			seen[edit.Path] = struct{}{}
		}
		if edit.OldPath != "" {
			seen[edit.OldPath] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func pathBaseForTestPath(t *testing.T, ctx context.Context, store *DB, baseCommitID, p string) *corev1.PathBase {
	t.Helper()
	base := &corev1.PathBase{
		Path:             p,
		BaseCommitId:     baseCommitID,
		EntryFingerprint: MissingEntryFingerprint(),
		Check:            "entry_fingerprint",
	}
	entry, err := store.Repository().GetEntry(ctx, baseCommitID, p)
	if errors.Is(err, ErrNotFound) {
		return base
	}
	if err != nil {
		t.Fatal(err)
	}
	base.Exists = true
	base.EntryKind = entry.Kind
	switch entry.Kind {
	case "directory":
		base.TreeId = entry.TreeID
		base.EntryFingerprint = DirectoryEntryFingerprint(entry.TreeID)
	default:
		base.Mode = entry.Mode
		base.BlobId = entry.BlobID
		base.ContentHash = entry.ContentHash
		base.EntryFingerprint = FileEntryFingerprint(FileEntry{
			Path:        entry.Path,
			BlobID:      entry.BlobID,
			ContentHash: entry.ContentHash,
			Mode:        entry.Mode,
			Size:        entry.Size,
		})
	}
	return base
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

func outboxDepthForTest(t *testing.T, ctx context.Context, store *DB) int {
	t.Helper()
	depth, err := store.Changesets().OutboxDepth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return depth
}

func outboxAttemptsForTest(t *testing.T, ctx context.Context, store *DB, id int64) int {
	t.Helper()
	var attempts int
	if err := store.db.QueryRowContext(ctx, `
		select attempts
		from outbox
		where id = $1
	`, id).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	return attempts
}

func commitChangedPathCountForTest(t *testing.T, ctx context.Context, store *DB, commitID, p string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `
		select count(*)
		from commit_changed_paths
		where target_ref = $1
		  and commit_id = $2
		  and path = $3
	`, DefaultTargetRef, commitID, p).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func commitChangedPathRowsForTest(t *testing.T, ctx context.Context, store *DB, targetRef string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `
		select commit_id, path, change_kind
		from commit_changed_paths
		where target_ref = $1
		order by commit_id, path, change_kind
	`, targetRef)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var commitID, p, changeKind string
		if err := rows.Scan(&commitID, &p, &changeKind); err != nil {
			t.Fatal(err)
		}
		out = append(out, commitID+"\x00"+p+"\x00"+changeKind)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
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
