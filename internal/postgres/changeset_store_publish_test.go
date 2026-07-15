package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestPublishPendingRevalidatesRefAfterTreeBuild(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)
	base := getTestRef(t, ctx, store)
	pendingPath := "/acme/payment/late_lock_pending.go"
	pendingBlobID, pendingHash := upsertTestBlobObject(t, ctx, store, objects, "package payment\nconst Pending = true\n")
	pending := createDraftPatchset(t, ctx, store, base.CommitId, pendingPath, pendingBlobID, pendingHash)
	if _, err := store.Changesets().Submit(ctx, pending.ChangesetId, pending.Id); err != nil {
		t.Fatal(err)
	}

	otherPath := "/acme/payment/late_lock_concurrent.go"
	otherBlobID, otherHash := upsertTestBlobObject(t, ctx, store, objects, "package payment\nconst Concurrent = true\n")
	otherCommitID := createDirectPublishTestCommit(t, ctx, store, base.CommitId, otherPath, otherBlobID, otherHash, int64(len("package payment\nconst Concurrent = true\n")))
	commitsBefore := countRowsForTest(t, ctx, store, `select count(*) from commits`)
	outboxBefore := outboxDepthForTest(t, ctx, store)

	hookStore := &publishHookObjectStore{
		base: objects,
		hook: func(ctx context.Context) error {
			res, err := store.db.ExecContext(ctx, `
				update refs
				set commit_id = $1, version = version + 1, updated_at = now(), updated_by = 'concurrent'
				where name = $2
			`, otherCommitID, DefaultTargetRef)
			if err != nil {
				return err
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				return fmt.Errorf("concurrent ref advance affected %d rows, want 1", affected)
			}
			return nil
		},
	}
	store.SetTreeStore(treestore.New(hookStore))

	published, err := store.Changesets().PublishPending(ctx, 10)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PublishPending err = %v, want ErrConflict", err)
	}
	if published != 0 {
		t.Fatalf("published after conflict = %d, want 0", published)
	}
	if got := getTestRef(t, ctx, store).CommitId; got != otherCommitID {
		t.Fatalf("ref after conflict = %s, want concurrent commit %s", got, otherCommitID)
	}
	if got := countRowsForTest(t, ctx, store, `select count(*) from commits`); got != commitsBefore {
		t.Fatalf("commit rows after conflict = %d, want %d", got, commitsBefore)
	}
	if got := outboxDepthForTest(t, ctx, store); got != outboxBefore {
		t.Fatalf("outbox depth after conflict = %d, want %d", got, outboxBefore)
	}
	pendingState := requirePublishStateForTest(t, ctx, store, pending.ChangesetId)
	if pendingState.PendingStatus != "pending" || pendingState.PendingCommit.Valid {
		t.Fatalf("pending state after conflict = %#v, want pending without commit", pendingState)
	}
	if pendingState.ChangesetStatus != "pending_publish" || pendingState.ChangesetCommit.Valid {
		t.Fatalf("changeset state after conflict = %#v, want pending_publish without commit", pendingState)
	}

	published, err = store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("published after retry = %d, want 1", published)
	}
	finalRef := getTestRef(t, ctx, store)
	finalCommit, err := store.Repository().GetCommit(ctx, finalRef.CommitId)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(finalCommit.ParentIds, []string{otherCommitID}) {
		t.Fatalf("retry commit parents = %#v, want [%s]", finalCommit.ParentIds, otherCommitID)
	}
	if _, err := store.Repository().GetFile(ctx, finalRef.CommitId, otherPath); err != nil {
		t.Fatalf("concurrent file missing after retry publish: %v", err)
	}
	if _, err := store.Repository().GetFile(ctx, finalRef.CommitId, pendingPath); err != nil {
		t.Fatalf("pending file missing after retry publish: %v", err)
	}
	pendingState = requirePublishStateForTest(t, ctx, store, pending.ChangesetId)
	if pendingState.PendingStatus != "published" || pendingState.PendingCommit.String != finalRef.CommitId {
		t.Fatalf("pending state after retry = %#v, want published at %s", pendingState, finalRef.CommitId)
	}
	if pendingState.ChangesetStatus != "submitted" || pendingState.ChangesetCommit.String != finalRef.CommitId {
		t.Fatalf("changeset state after retry = %#v, want submitted at %s", pendingState, finalRef.CommitId)
	}
}

func TestPublishPendingBatchPublishesAndSkipsNonPendingChangesets(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstPath := "/acme/payment/batch_publish_first.go"
	secondPath := "/acme/payment/batch_publish_second.go"
	skippedPath := "/acme/payment/batch_publish_skipped.go"
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst First = true\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst Second = true\n")
	skippedBlobID, skippedHash := upsertTestBlob(t, ctx, store, "package payment\nconst Skipped = true\n")

	first := createDraftPatchset(t, ctx, store, base.CommitId, firstPath, firstBlobID, firstHash)
	second := createDraftPatchset(t, ctx, store, base.CommitId, secondPath, secondBlobID, secondHash)
	skipped := createDraftPatchset(t, ctx, store, base.CommitId, skippedPath, skippedBlobID, skippedHash)
	for _, patchset := range []*corev1.Patchset{first, second, skipped} {
		if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
		update changesets
		set status = 'draft', updated_at = now()
		where id = $1
	`, skipped.ChangesetId); err != nil {
		t.Fatal(err)
	}

	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 2 {
		t.Fatalf("published = %d, want 2", published)
	}

	firstState := requirePublishStateForTest(t, ctx, store, first.ChangesetId)
	secondState := requirePublishStateForTest(t, ctx, store, second.ChangesetId)
	skippedState := requirePublishStateForTest(t, ctx, store, skipped.ChangesetId)
	if firstState.ChangesetStatus != "submitted" || firstState.PendingStatus != "published" || !firstState.PendingCommit.Valid {
		t.Fatalf("first state = %#v, want submitted/published with commit", firstState)
	}
	if secondState.ChangesetStatus != "submitted" || secondState.PendingStatus != "published" || !secondState.PendingCommit.Valid {
		t.Fatalf("second state = %#v, want submitted/published with commit", secondState)
	}
	if skippedState.ChangesetStatus != "draft" || skippedState.PendingStatus != "pending" || skippedState.PendingCommit.Valid {
		t.Fatalf("skipped state = %#v, want draft/pending without commit", skippedState)
	}

	ref := getTestRef(t, ctx, store)
	if ref.CommitId != secondState.PendingCommit.String {
		t.Fatalf("ref commit = %s, want second published commit %s", ref.CommitId, secondState.PendingCommit.String)
	}
	requireCommitParentsForTest(t, ctx, store, firstState.PendingCommit.String, []string{base.CommitId})
	requireCommitParentsForTest(t, ctx, store, secondState.PendingCommit.String, []string{firstState.PendingCommit.String})
	for _, p := range []string{firstPath, secondPath} {
		if _, err := store.Repository().GetFile(ctx, ref.CommitId, p); err != nil {
			t.Fatalf("GetFile(%s): %v", p, err)
		}
	}
	if _, err := store.Repository().GetFile(ctx, ref.CommitId, skippedPath); !errors.Is(err, ErrNotFound) {
		t.Fatalf("skipped GetFile err = %v, want ErrNotFound", err)
	}
	requireOutboxPayloadsForTest(t, ctx, store, []publishOutboxWant{
		{
			TargetRef:    DefaultTargetRef,
			CommitID:     firstState.PendingCommit.String,
			BaseCommitID: base.CommitId,
			ChangesetID:  first.ChangesetId,
			PatchsetID:   first.Id,
		},
		{
			TargetRef:    DefaultTargetRef,
			CommitID:     secondState.PendingCommit.String,
			BaseCommitID: firstState.PendingCommit.String,
			ChangesetID:  second.ChangesetId,
			PatchsetID:   second.Id,
		},
	})
}

type publishHookObjectStore struct {
	base  treestore.ObjectStore
	hook  func(context.Context) error
	fired atomic.Bool
}

func (s *publishHookObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := s.maybeHook(ctx); err != nil {
		return err
	}
	return s.base.Put(ctx, key, r)
}

func (s *publishHookObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := s.maybeHook(ctx); err != nil {
		return nil, err
	}
	return s.base.Get(ctx, key, offset, length)
}

func (s *publishHookObjectStore) maybeHook(ctx context.Context) error {
	if s.hook == nil || !s.fired.CompareAndSwap(false, true) {
		return nil
	}
	return s.hook(ctx)
}

type publishStateForTest struct {
	ChangesetStatus string
	ChangesetCommit sql.NullString
	PendingStatus   string
	PendingCommit   sql.NullString
}

func requirePublishStateForTest(t *testing.T, ctx context.Context, store *DB, changesetID string) publishStateForTest {
	t.Helper()
	var state publishStateForTest
	if err := store.db.QueryRowContext(ctx, `
		select c.status, c.commit_id, pending.status, pending.commit_id
		from changesets c
		join pending_publish pending on pending.changeset_id = c.id
		where c.id = $1
	`, changesetID).Scan(&state.ChangesetStatus, &state.ChangesetCommit, &state.PendingStatus, &state.PendingCommit); err != nil {
		t.Fatal(err)
	}
	return state
}

func createDirectPublishTestCommit(t *testing.T, ctx context.Context, store *DB, baseCommitID, p, blobID, contentHash string, size int64) string {
	t.Helper()
	rootTreeID, err := store.Repository().RootTreeForCommit(ctx, baseCommitID)
	if err != nil {
		t.Fatal(err)
	}
	rootTreeID, err = store.changesets.trees.ApplyEdits(ctx, rootTreeID, []treestore.FileEdit{{
		Op:   "upsert",
		Path: p,
		File: &treestore.FileEntry{
			Path:        p,
			BlobID:      blobID,
			ContentHash: contentHash,
			Mode:        0o100644,
			Size:        size,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	parentJSON, err := encodeJSON([]string{baseCommitID})
	if err != nil {
		t.Fatal(err)
	}
	changedJSON, err := encodeJSON([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	commitID := objectid.CommitID(objectid.CommitObject{
		ParentIDs:    []string{baseCommitID},
		RootTreeID:   rootTreeID,
		Author:       "user_bob",
		Message:      "concurrent advance",
		CreatedAt:    now,
		ChangedPaths: []string{p},
	})
	if _, err := store.db.ExecContext(ctx, `
		insert into commits(id, parent_ids, root_tree_id, author_subject_id, message, created_at, changed_paths)
		values ($1, $2, $3, $4, $5, $6, $7)
	`, commitID, parentJSON, rootTreeID, "user_bob", "concurrent advance", now, changedJSON); err != nil {
		t.Fatal(err)
	}
	return commitID
}

func countRowsForTest(t *testing.T, ctx context.Context, store *DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func requireCommitParentsForTest(t *testing.T, ctx context.Context, store *DB, commitID string, want []string) {
	t.Helper()
	commit, err := store.Repository().GetCommit(ctx, commitID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commit.ParentIds, want) {
		t.Fatalf("commit %s parents = %#v, want %#v", commitID, commit.ParentIds, want)
	}
}

type publishOutboxWant struct {
	TargetRef    string
	CommitID     string
	BaseCommitID string
	ChangesetID  string
	PatchsetID   string
}

func requireOutboxPayloadsForTest(t *testing.T, ctx context.Context, store *DB, want []publishOutboxWant) {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `
		select payload->>'target_ref',
		       payload->>'commit_id',
		       payload->>'base_commit_id',
		       payload->>'changeset_id',
		       payload->>'patchset_id'
		from outbox
		where kind = $1
		order by id
	`, outboxKindCommitPublished)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []publishOutboxWant
	for rows.Next() {
		var row publishOutboxWant
		if err := rows.Scan(&row.TargetRef, &row.CommitID, &row.BaseCommitID, &row.ChangesetID, &row.PatchsetID); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outbox payloads = %#v, want %#v", got, want)
	}
}
