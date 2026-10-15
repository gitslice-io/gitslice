package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) Submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (res *corev1.SubmitChangesetResponse, err error) {
	return s.SubmitWithCheckStatuses(ctx, changesetID, expectedCurrentPatchsetID, nil)
}

func (s *ChangesetStore) SetPendingPublishListener(fn func()) {
	s.onPendingPublish = fn
}

func (s *ChangesetStore) SubmitWithCheckStatuses(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (res *corev1.SubmitChangesetResponse, err error) {
	defer func() {
		storage.RecordSubmitResult(err)
	}()
retry:
	for attempt := 0; attempt < submitRetryAttempts; attempt++ {
		res, err = s.submitOnce(ctx, changesetID, expectedCurrentPatchsetID, extraCheckStatuses)
		if err == nil || !isRetryableSerializationError(err) || attempt == submitRetryAttempts-1 {
			break
		}
		wait := time.Duration(attempt+1) * submitRetryBaseWait
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			res, err = nil, ctx.Err()
			break retry
		case <-timer.C:
		}
	}
	if err == nil && res != nil && res.Status == "pending_publish" && s.onPendingPublish != nil {
		s.onPendingPublish()
	}
	return res, err
}

func (s *ChangesetStore) submitOnce(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (*corev1.SubmitChangesetResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var cs struct {
		ID                string
		Status            string
		CurrentPatchsetID string
		TargetRef         string
		BaseCommitID      string
		Author            string
		Title             string
		Account           string
		Slice             string
		SliceID           string
		Number            int64
		CommitID          sql.NullString
		ParentChangesetID sql.NullString
	}
	err = tx.QueryRowContext(ctx, `
			select id, status, coalesce(current_patchset_id, ''), target_ref,
			       base_commit_id, author_subject_id, title, authoring_account, authoring_slice, authoring_slice_id, number, commit_id,
			       parent_changeset_id
			from changesets
			where id = $1
			for update
	`, changesetID).Scan(&cs.ID, &cs.Status, &cs.CurrentPatchsetID, &cs.TargetRef,
		&cs.BaseCommitID, &cs.Author, &cs.Title, &cs.Account, &cs.Slice, &cs.SliceID, &cs.Number, &cs.CommitID,
		&cs.ParentChangesetID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cs.Status == "abandoned" {
		return nil, ErrConflict
	}
	if cs.Status == "submitted" && cs.CommitID.Valid {
		return &corev1.SubmitChangesetResponse{CommitId: cs.CommitID.String, TargetRef: cs.TargetRef, NewRefCommitId: cs.CommitID.String, Status: "submitted"}, nil
	}
	if cs.Status == "pending_publish" {
		var pendingID string
		err := tx.QueryRowContext(ctx, `
			select id
			from pending_publish
			where changeset_id = $1 and status = 'pending'
			`, cs.ID).Scan(&pendingID)
		if err == nil {
			return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: "pending_publish", PendingPublishId: pendingID}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, ErrConflict
	}
	if expectedCurrentPatchsetID != "" && cs.CurrentPatchsetID != expectedCurrentPatchsetID {
		return nil, ErrConflict
	}
	if cs.CurrentPatchsetID == "" {
		return nil, ErrConflict
	}
	patchset, err := getPatchsetTx(ctx, tx, cs.CurrentPatchsetID)
	if err != nil {
		return nil, err
	}
	if len(patchset.Conflicts) > 0 {
		return nil, fmt.Errorf("%w: unresolved patchset conflicts", ErrConflict)
	}
	if cs.ParentChangesetID.Valid {
		var parentStatus string
		err = tx.QueryRowContext(ctx, `
			select status
			from changesets
			where id = $1
			for update
		`, cs.ParentChangesetID.String).Scan(&parentStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if parentStatus != "submitted" && parentStatus != "pending_publish" {
			return nil, blockSubmitTx(ctx, tx, cs.ID, "BlockedOnBaseChangeset")
		}
	}
	latestReq, latestIncludedPaths, err := submitRequirementsAndIncludedPathsForSliceTx(ctx, tx, cs.SliceID)
	if err != nil {
		return nil, err
	}
	for _, p := range patchset.ChangedPaths {
		if !pathInAnyPrefix(latestIncludedPaths, p) {
			return nil, blockSubmitTx(ctx, tx, cs.ID, fmt.Sprintf("changed path %s is outside latest slice definition, refresh the changeset", p))
		}
	}
	approvalSubjects, err := approvalSubjectsTx(ctx, tx, cs.ID, patchset.Id)
	if err != nil {
		return nil, err
	}
	checkStatuses, err := checkStatusesTx(ctx, tx, cs.ID, patchset.Id)
	if err != nil {
		return nil, err
	}
	mergeCheckStatuses(checkStatuses, extraCheckStatuses)
	if reason := storage.EvaluateSubmitRequirements(latestReq, cs.Author, approvalSubjects, checkStatuses); reason != "" {
		return nil, blockSubmitTx(ctx, tx, cs.ID, reason)
	}
	var currentCommitID string
	err = tx.QueryRowContext(ctx, `
		select commit_id
		from refs
		where name = $1
	`, cs.TargetRef).Scan(&currentCommitID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.validateAcceptedPathBasesTx(ctx, tx, cs.TargetRef, currentCommitID, patchset.PathBases); err != nil {
		if !errors.Is(err, ErrConflict) {
			return nil, err
		}
		return nil, blockSubmitTx(ctx, tx, cs.ID, "path base conflict, refresh or rebase the changeset")
	}
	if err := applyPathHeadEditsTx(ctx, tx, cs.ID, patchset.Id, patchset.FileEdits); err != nil {
		return nil, err
	}
	pendingID, err := objectid.RandomID("pub")
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		insert into pending_publish(id, changeset_id, patchset_id, target_ref, base_ref_commit_id, status, created_at, updated_at)
		values ($1, $2, $3, $4, $5, 'pending', now(), now())
	`, pendingID, cs.ID, patchset.Id, cs.TargetRef, currentCommitID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		update changesets
		set status = 'pending_publish',
		    submit_blocked_reason = '',
		    updated_at = now()
		where id = $1
	`, cs.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.SubmitChangesetResponse{TargetRef: cs.TargetRef, Status: "pending_publish", PendingPublishId: pendingID}, nil
}
