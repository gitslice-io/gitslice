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

func TestPublishPendingMultiChangesetDeterministicChainMovesRefOnce(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	firstPath := "/acme/payment/deterministic_first.go"
	secondPath := "/acme/payment/deterministic_second.go"
	firstBlobID, firstHash := upsertTestBlob(t, ctx, store, "package payment\nconst DeterministicFirst = true\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "package payment\nconst DeterministicSecond = true\n")
	first := createDraftPatchset(t, ctx, store, base.CommitId, firstPath, firstBlobID, firstHash)
	second := createDraftPatchset(t, ctx, store, base.CommitId, secondPath, secondBlobID, secondHash)
	for _, patchset := range []*corev1.Patchset{first, second} {
		if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
			t.Fatal(err)
		}
	}
	versionBefore := refVersionForPublishTest(t, ctx, store)

	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 2 {
		t.Fatalf("published = %d, want 2", published)
	}

	firstState := requirePublishStateForTest(t, ctx, store, first.ChangesetId)
	secondState := requirePublishStateForTest(t, ctx, store, second.ChangesetId)
	firstCommit := commitInputsForPublishTest(t, ctx, store, firstState.PendingCommit.String)
	secondCommit := commitInputsForPublishTest(t, ctx, store, secondState.PendingCommit.String)
	wantFirstID := objectid.CommitID(objectid.CommitObject{
		ParentIDs:    []string{base.CommitId},
		RootTreeID:   firstCommit.RootTreeID,
		Author:       "user_alice",
		Message:      "storage test",
		CreatedAt:    firstCommit.CreatedAt,
		ChangedPaths: []string{firstPath},
	})
	wantSecondID := objectid.CommitID(objectid.CommitObject{
		ParentIDs:    []string{wantFirstID},
		RootTreeID:   secondCommit.RootTreeID,
		Author:       "user_alice",
		Message:      "storage test",
		CreatedAt:    secondCommit.CreatedAt,
		ChangedPaths: []string{secondPath},
	})
	if firstState.PendingCommit.String != wantFirstID {
		t.Fatalf("first commit id = %s, want deterministic id %s", firstState.PendingCommit.String, wantFirstID)
	}
	if secondState.PendingCommit.String != wantSecondID {
		t.Fatalf("second commit id = %s, want deterministic id %s", secondState.PendingCommit.String, wantSecondID)
	}
	if got := secondCommit.CreatedAt.Sub(firstCommit.CreatedAt); got != time.Microsecond {
		t.Fatalf("commit timestamp step = %s, want 1us", got)
	}
	if got := getTestRef(t, ctx, store).CommitId; got != wantSecondID {
		t.Fatalf("ref commit = %s, want final deterministic commit %s", got, wantSecondID)
	}
	if got := refVersionForPublishTest(t, ctx, store); got != versionBefore+1 {
		t.Fatalf("ref version = %d, want one move to %d", got, versionBefore+1)
	}
	for _, p := range []string{firstPath, secondPath} {
		if _, err := store.Repository().GetFile(ctx, wantSecondID, p); err != nil {
			t.Fatalf("GetFile(%s): %v", p, err)
		}
	}
}

func TestPublishPendingConcurrentWorkersDoNotDoublePublishOrLoseRows(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)
	base := getTestRef(t, ctx, store)
	firstPath := "/acme/payment/concurrent_first.go"
	secondPath := "/acme/payment/concurrent_second.go"
	firstBlobID, firstHash := upsertTestBlobObject(t, ctx, store, objects, "package payment\nconst ConcurrentFirst = true\n")
	secondBlobID, secondHash := upsertTestBlobObject(t, ctx, store, objects, "package payment\nconst ConcurrentSecond = true\n")
	first := createDraftPatchset(t, ctx, store, base.CommitId, firstPath, firstBlobID, firstHash)
	second := createDraftPatchset(t, ctx, store, base.CommitId, secondPath, secondBlobID, secondHash)
	for _, patchset := range []*corev1.Patchset{first, second} {
		if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
			t.Fatal(err)
		}
	}
	versionBefore := refVersionForPublishTest(t, ctx, store)

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	store.SetTreeStore(treestore.New(&blockingPublishObjectStore{
		base:    objects,
		entered: entered,
		release: release,
	}))
	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	type result struct {
		published int
		err       error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			published, err := store.Changesets().PublishPending(testCtx, 1)
			results <- result{published: published, err: err}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-testCtx.Done():
			t.Fatalf("workers did not both reach transaction-free tree build: %v", testCtx.Err())
		}
	}
	close(release)
	released = true

	var got []result
	for i := 0; i < 2; i++ {
		select {
		case res := <-results:
			got = append(got, res)
		case <-testCtx.Done():
			t.Fatalf("PublishPending workers did not settle: %v", testCtx.Err())
		}
	}
	wins, conflicts := 0, 0
	for _, res := range got {
		switch {
		case res.err == nil && res.published == 1:
			wins++
		case errors.Is(res.err, ErrConflict) && res.published == 0:
			conflicts++
		default:
			t.Fatalf("concurrent PublishPending result = %#v, want one publish or ErrConflict", res)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = %#v, want one winner and one conflict", got)
	}
	assertPendingPublishStatusCounts(t, ctx, store, 1, 0, 1)

	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("retry published = %d, want remaining row", published)
	}
	assertPendingPublishStatusCounts(t, ctx, store, 0, 0, 2)
	for _, patchset := range []*corev1.Patchset{first, second} {
		state := requirePublishStateForTest(t, ctx, store, patchset.ChangesetId)
		if state.ChangesetStatus != "submitted" || state.PendingStatus != "published" || !state.PendingCommit.Valid {
			t.Fatalf("final publish state = %#v, want submitted/published", state)
		}
	}
	var publishedRows, distinctCommits int
	if err := store.db.QueryRowContext(ctx, `
		select count(*), count(distinct commit_id)
		from pending_publish
		where status = 'published'
	`).Scan(&publishedRows, &distinctCommits); err != nil {
		t.Fatal(err)
	}
	if publishedRows != 2 || distinctCommits != 2 {
		t.Fatalf("published rows/distinct commits = %d/%d, want 2/2", publishedRows, distinctCommits)
	}
	if got := outboxDepthForTest(t, ctx, store); got != 2 {
		t.Fatalf("commit-published outbox rows = %d, want 2", got)
	}
	if got := refVersionForPublishTest(t, ctx, store); got != versionBefore+2 {
		t.Fatalf("ref version = %d, want exactly two moves to %d", got, versionBefore+2)
	}
	finalRef := getTestRef(t, ctx, store)
	for _, p := range []string{firstPath, secondPath} {
		if _, err := store.Repository().GetFile(ctx, finalRef.CommitId, p); err != nil {
			t.Fatalf("GetFile(%s): %v", p, err)
		}
	}
}

func TestPublishPendingReapsStaleClaim(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	p := "/acme/payment/reaped_claim.go"
	blobID, contentHash := upsertTestBlob(t, ctx, store, "package payment\nconst ReapedClaim = true\n")
	patchset := createDraftPatchset(t, ctx, store, base.CommitId, p, blobID, contentHash)
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		update pending_publish
		set status = 'publishing', updated_at = now() - interval '5 minutes'
		where changeset_id = $1
	`, patchset.ChangesetId); err != nil {
		t.Fatal(err)
	}

	published, err := store.Changesets().PublishPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("published = %d, want reclaimed row", published)
	}
	state := requirePublishStateForTest(t, ctx, store, patchset.ChangesetId)
	if state.ChangesetStatus != "submitted" || state.PendingStatus != "published" || !state.PendingCommit.Valid {
		t.Fatalf("state after stale-claim reaping = %#v, want submitted/published", state)
	}
	if got := getTestRef(t, ctx, store).CommitId; got != state.PendingCommit.String {
		t.Fatalf("ref commit = %s, want reclaimed commit %s", got, state.PendingCommit.String)
	}
}

type publishHookObjectStore struct {
	base  treestore.ObjectStore
	hook  func(context.Context) error
	fired atomic.Bool
}

type blockingPublishObjectStore struct {
	base    treestore.ObjectStore
	entered chan<- struct{}
	release <-chan struct{}
	calls   atomic.Int32
}

func (s *blockingPublishObjectStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := s.maybeBlock(ctx); err != nil {
		return err
	}
	return s.base.Put(ctx, key, r)
}

func (s *blockingPublishObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := s.maybeBlock(ctx); err != nil {
		return nil, err
	}
	return s.base.Get(ctx, key, offset, length)
}

func (s *blockingPublishObjectStore) maybeBlock(ctx context.Context) error {
	if s.calls.Add(1) > 2 {
		return nil
	}
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

type commitInputsForPublish struct {
	RootTreeID string
	CreatedAt  time.Time
}

func commitInputsForPublishTest(t *testing.T, ctx context.Context, store *DB, commitID string) commitInputsForPublish {
	t.Helper()
	var inputs commitInputsForPublish
	if err := store.db.QueryRowContext(ctx, `
		select root_tree_id, created_at
		from commits
		where id = $1
	`, commitID).Scan(&inputs.RootTreeID, &inputs.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return inputs
}

func refVersionForPublishTest(t *testing.T, ctx context.Context, store *DB) int64 {
	t.Helper()
	var version int64
	if err := store.db.QueryRowContext(ctx, `
		select version
		from refs
		where name = $1
	`, DefaultTargetRef).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func assertPendingPublishStatusCounts(t *testing.T, ctx context.Context, store *DB, pending, publishing, published int) {
	t.Helper()
	var gotPending, gotPublishing, gotPublished int
	if err := store.db.QueryRowContext(ctx, `
		select count(*) filter (where status = 'pending'),
		       count(*) filter (where status = 'publishing'),
		       count(*) filter (where status = 'published')
		from pending_publish
	`).Scan(&gotPending, &gotPublishing, &gotPublished); err != nil {
		t.Fatal(err)
	}
	if gotPending != pending || gotPublishing != publishing || gotPublished != published {
		t.Fatalf("pending publish statuses = pending:%d publishing:%d published:%d, want %d/%d/%d",
			gotPending, gotPublishing, gotPublished, pending, publishing, published)
	}
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
