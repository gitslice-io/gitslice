package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	outboxKindCommitPublished = "commit_published"

	submitRetryAttempts        = 5
	submitRetryBaseWait        = 5 * time.Millisecond
	pendingPublishClaimTimeout = 60 * time.Second
)

func isRetryableSerializationError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

type commitPublishedPayload struct {
	TargetRef    string    `json:"target_ref"`
	CommitID     string    `json:"commit_id"`
	BaseCommitID string    `json:"base_commit_id"`
	ChangesetID  string    `json:"changeset_id"`
	PatchsetID   string    `json:"patchset_id"`
	ChangedPaths []string  `json:"changed_paths"`
	CommittedAt  time.Time `json:"committed_at"`
}

type publishCommitInsert struct {
	ID              string
	ParentIDsJSON   []byte
	RootTreeID      string
	AuthorSubjectID string
	Message         string
	CreatedAt       time.Time
	ChangedJSON     []byte
}

type publishedPendingUpdate struct {
	PendingID string
	CommitID  string
}

type submittedChangesetUpdate struct {
	ChangesetID       string
	CommitID          string
	StackID           string
	ObservedUpdatedAt time.Time
}

type pendingPublishClaim struct {
	Rows             []pendingPublishRow
	TargetRef        string
	OriginalCommitID string
	RootTreeID       string
}

type pendingPublishBuild struct {
	CurrentCommitID   string
	UpdatedBy         string
	CommitInserts     []publishCommitInsert
	PathHeadMutations []publishPathHeadMutation
	OutboxPayloads    []commitPublishedPayload
	PendingUpdates    []publishedPendingUpdate
	ChangesetUpdates  []submittedChangesetUpdate
	SkippedPendingIDs []string
	PublishLatencies  []time.Duration
}

type publishPathHeadMutation struct {
	Heads       []PathHead
	DeletePath  string
	ChangesetID string
	PatchsetID  string
}

func (s *ChangesetStore) PublishPending(ctx context.Context, limit int) (published int, err error) {
	defer func() {
		storage.RecordPublishBatch(published, err)
	}()
	if s.trees == nil {
		return 0, fmt.Errorf("tree store is not configured")
	}
	if limit <= 0 {
		limit = 64
	}
	if err := s.reapStalePendingPublishClaims(ctx); err != nil {
		return 0, err
	}
	claim, err := s.claimPendingPublishBatch(ctx, limit)
	if err != nil {
		return 0, err
	}
	if len(claim.Rows) == 0 {
		return 0, nil
	}
	build, err := s.buildPendingPublishBatch(ctx, claim)
	if err != nil {
		s.releasePendingPublishClaims(ctx, claim)
		return 0, err
	}
	if err := s.finalizePendingPublishBatch(ctx, claim, build); err != nil {
		s.releasePendingPublishClaims(ctx, claim)
		return 0, err
	}
	for _, latency := range build.PublishLatencies {
		storage.ObservePublishLatency(latency)
	}
	return len(build.PendingUpdates), nil
}

// releasePendingPublishClaims returns a failed batch's claimed rows to
// 'pending' so the next publisher pass retries them, instead of leaving them in
// 'publishing' until reapStalePendingPublishClaims times them out. Rows the
// batch already published are untouched (the reset only matches 'publishing').
// It uses a fresh context because the failure may be ctx cancellation.
func (s *ChangesetStore) releasePendingPublishClaims(ctx context.Context, claim *pendingPublishClaim) {
	ids := make([]string, 0, len(claim.Rows))
	for _, row := range claim.Rows {
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(releaseCtx, `
		update pending_publish
		set status = 'pending', updated_at = now()
		where id = any($1) and status = 'publishing'
	`, ids); err != nil {
		slog.Warn("release pending publish claims failed", "error", err, "rows", len(ids))
	}
}

func (s *ChangesetStore) reapStalePendingPublishClaims(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		update pending_publish
		set status = 'pending', updated_at = now()
		where status = 'publishing'
		  and updated_at < now() - ($1::bigint * interval '1 second')
	`, int64(pendingPublishClaimTimeout/time.Second))
	return err
}

func (s *ChangesetStore) claimPendingPublishBatch(ctx context.Context, limit int) (*pendingPublishClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		select id, changeset_id, patchset_id, target_ref, created_at
		from pending_publish
		where status = 'pending'
		order by sequence
		limit $1
		for update skip locked
	`, limit)
	if err != nil {
		return nil, err
	}
	var pending []pendingPublishRow
	for rows.Next() {
		var row pendingPublishRow
		if err := rows.Scan(&row.ID, &row.ChangesetID, &row.PatchsetID, &row.TargetRef, &row.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pending = append(pending, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &pendingPublishClaim{}, nil
	}
	targetRef := pending[0].TargetRef
	batch := make([]pendingPublishRow, 0, len(pending))
	for _, row := range pending {
		if row.TargetRef == targetRef {
			batch = append(batch, row)
		}
	}
	var originalCommitID, rootTreeID string
	err = tx.QueryRowContext(ctx, `
		select ref.commit_id, commit.root_tree_id
		from refs ref
		join commits commit on commit.id = ref.commit_id
		where ref.name = $1
	`, targetRef).Scan(&originalCommitID, &rootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	claimedIDs := make([]string, 0, len(batch))
	for _, row := range batch {
		claimedIDs = append(claimedIDs, row.ID)
	}
	res, err := tx.ExecContext(ctx, `
		update pending_publish
		set status = 'publishing', updated_at = now()
		where id = any($1) and status = 'pending'
	`, claimedIDs)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != int64(len(batch)) {
		return nil, fmt.Errorf("claimed %d pending publish rows, want %d", affected, len(batch))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &pendingPublishClaim{
		Rows:             batch,
		TargetRef:        targetRef,
		OriginalCommitID: originalCommitID,
		RootTreeID:       rootTreeID,
	}, nil
}

func (s *ChangesetStore) buildPendingPublishBatch(ctx context.Context, claim *pendingPublishClaim) (*pendingPublishBuild, error) {
	build := &pendingPublishBuild{
		CurrentCommitID: claim.OriginalCommitID,
		UpdatedBy:       "publisher",
	}
	baseTime := time.Now().UTC().Truncate(time.Microsecond)
	type changesetFields struct {
		Author      string
		Title       string
		Description string
		Status      string
		StackID     string
		UpdatedAt   time.Time
	}
	type publishableEntry struct {
		row       pendingPublishRow
		cs        changesetFields
		patchset  *corev1.Patchset
		treeEdits []treestore.FileEdit
	}
	publishable := make([]publishableEntry, 0, len(claim.Rows))
	editSets := make([][]treestore.FileEdit, 0, len(claim.Rows))
	for _, row := range claim.Rows {
		var cs changesetFields
		err := s.db.QueryRowContext(ctx, `
				select author_subject_id, title, description, status, coalesce(stack_id, ''), updated_at
				from changesets
				where id = $1
			`, row.ChangesetID).Scan(&cs.Author, &cs.Title, &cs.Description, &cs.Status, &cs.StackID, &cs.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if cs.Status != "pending_publish" {
			build.SkippedPendingIDs = append(build.SkippedPendingIDs, row.ID)
			continue
		}
		patchset, err := getPatchset(ctx, s.db, row.PatchsetID)
		if err != nil {
			return nil, err
		}
		treeEdits, err := treeEditsFromPatchset(ctx, s.db, patchset.FileEdits)
		if err != nil {
			return nil, err
		}
		publishable = append(publishable, publishableEntry{row: row, cs: cs, patchset: patchset, treeEdits: treeEdits})
		editSets = append(editSets, treeEdits)
	}
	roots, err := s.trees.ApplyEditChain(ctx, claim.RootTreeID, editSets)
	if errors.Is(err, treestore.ErrNotFound) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	for i, entry := range publishable {
		row := entry.row
		cs := entry.cs
		patchset := entry.patchset
		rootTreeID := roots[i]
		baseCommitID := build.CurrentCommitID
		now := baseTime.Add(time.Duration(len(build.PendingUpdates)) * time.Microsecond)
		message := storage.CommitMessage(cs.Title, cs.Description)
		if message == "" {
			message = "Submit " + storage.ShortChangesetID(row.ChangesetID)
		}
		commitID := objectid.CommitID(objectid.CommitObject{
			ParentIDs:    []string{build.CurrentCommitID},
			RootTreeID:   rootTreeID,
			Author:       cs.Author,
			Message:      message,
			CreatedAt:    now,
			ChangedPaths: patchset.ChangedPaths,
		})
		parentJSON, err := encodeJSON([]string{build.CurrentCommitID})
		if err != nil {
			return nil, err
		}
		changedJSON, err := encodeJSON(patchset.ChangedPaths)
		if err != nil {
			return nil, err
		}
		build.CommitInserts = append(build.CommitInserts, publishCommitInsert{
			ID:              commitID,
			ParentIDsJSON:   parentJSON,
			RootTreeID:      rootTreeID,
			AuthorSubjectID: cs.Author,
			Message:         message,
			CreatedAt:       now,
			ChangedJSON:     changedJSON,
		})
		mutations, err := s.pathHeadMutationsForPatchsetRoot(ctx, rootTreeID, row.ChangesetID, row.PatchsetID, patchset.FileEdits)
		if err != nil {
			return nil, err
		}
		build.PathHeadMutations = append(build.PathHeadMutations, mutations...)
		build.OutboxPayloads = append(build.OutboxPayloads, commitPublishedPayload{
			TargetRef:    claim.TargetRef,
			CommitID:     commitID,
			BaseCommitID: baseCommitID,
			ChangesetID:  row.ChangesetID,
			PatchsetID:   row.PatchsetID,
			ChangedPaths: patchset.ChangedPaths,
			CommittedAt:  now,
		})
		build.PendingUpdates = append(build.PendingUpdates, publishedPendingUpdate{PendingID: row.ID, CommitID: commitID})
		build.ChangesetUpdates = append(build.ChangesetUpdates, submittedChangesetUpdate{
			ChangesetID:       row.ChangesetID,
			CommitID:          commitID,
			StackID:           cs.StackID,
			ObservedUpdatedAt: cs.UpdatedAt,
		})
		build.CurrentCommitID = commitID
		build.UpdatedBy = cs.Author
		build.PublishLatencies = append(build.PublishLatencies, time.Since(row.CreatedAt))
	}
	return build, nil
}

func (s *ChangesetStore) finalizePendingPublishBatch(ctx context.Context, claim *pendingPublishClaim, build *pendingPublishBuild) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	claimedIDs := make([]string, 0, len(claim.Rows))
	for _, row := range claim.Rows {
		claimedIDs = append(claimedIDs, row.ID)
	}
	if len(build.PendingUpdates) == 0 {
		if err := resetPendingPublishClaimsTx(ctx, tx, claimedIDs); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	}
	// FOR NO KEY UPDATE, not FOR UPDATE: the publisher only moves commit_id, and
	// a full FOR UPDATE also blocks the FOR KEY SHARE that every pending_publish
	// insert takes through its target_ref foreign key. Submit locks changesets
	// and then inserts pending_publish (-> refs), while this transaction locks
	// refs and then changesets, so FOR UPDATE here deadlocked with concurrent
	// submits (e.g. SubmitStack while an earlier entry publishes).
	var lockedCommitID string
	err = tx.QueryRowContext(ctx, `
		select commit_id
		from refs
		where name = $1
		for no key update
	`, claim.TargetRef).Scan(&lockedCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if lockedCommitID != claim.OriginalCommitID {
		if err := resetPendingPublishClaimsTx(ctx, tx, claimedIDs); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		storage.RecordRefCASFailure()
		return ErrConflict
	}
	if err := lockPendingPublishChangesetsTx(ctx, tx, build.ChangesetUpdates); err != nil {
		if errors.Is(err, ErrConflict) {
			if resetErr := resetPendingPublishClaimsTx(ctx, tx, claimedIDs); resetErr != nil {
				return resetErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return commitErr
			}
		}
		return err
	}
	if err := insertPublishedCommitsTx(ctx, tx, build.CommitInserts); err != nil {
		return err
	}
	if err := applyPublishPathHeadMutationsTx(ctx, tx, build.PathHeadMutations); err != nil {
		return err
	}
	if err := markPendingPublishedTx(ctx, tx, build.PendingUpdates); err != nil {
		return err
	}
	if err := resetPendingPublishClaimsTx(ctx, tx, build.SkippedPendingIDs); err != nil {
		return err
	}
	if err := markChangesetsSubmittedTx(ctx, tx, build.ChangesetUpdates); err != nil {
		return err
	}
	if err := refreshClosedStackStatusesForSubmittedTx(ctx, tx, build.ChangesetUpdates); err != nil {
		return err
	}
	if err := insertCommitPublishedOutboxTx(ctx, tx, build.OutboxPayloads); err != nil {
		return err
	}
	if err := insertSubmittedWebhookEventsTx(ctx, tx, build); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		update refs
		set commit_id = $1, version = version + 1, updated_at = now(), updated_by = $2
		where name = $3 and commit_id = $4
	`, build.CurrentCommitID, build.UpdatedBy, claim.TargetRef, claim.OriginalCommitID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		storage.RecordRefCASFailure()
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *ChangesetStore) PendingPublishDepth(ctx context.Context) (int, error) {
	var depth int
	err := s.db.QueryRowContext(ctx, `
		select count(*)
		from pending_publish
		where status = 'pending'
	`).Scan(&depth)
	return depth, err
}
