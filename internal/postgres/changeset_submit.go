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
	return s.submit(ctx, changesetID, expectedCurrentPatchsetID, extraCheckStatuses, false)
}

// SubmitBypassingRequirements submits without the slice's required approvals
// and checks. The service uses it only for a changeset that is a single copy
// or move (CopyPath, MovePath); path bases and the slice definition still
// apply.
func (s *ChangesetStore) SubmitBypassingRequirements(ctx context.Context, changesetID, expectedCurrentPatchsetID string) (*corev1.SubmitChangesetResponse, error) {
	return s.submit(ctx, changesetID, expectedCurrentPatchsetID, nil, true)
}

func (s *ChangesetStore) submit(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string, bypassRequirements bool) (res *corev1.SubmitChangesetResponse, err error) {
	defer func() {
		storage.RecordSubmitResult(err)
	}()
retry:
	for attempt := 0; attempt < submitRetryAttempts; attempt++ {
		res, err = s.submitOnce(ctx, changesetID, expectedCurrentPatchsetID, extraCheckStatuses, bypassRequirements)
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

func (s *ChangesetStore) submitOnce(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string, bypassRequirements bool) (*corev1.SubmitChangesetResponse, error) {
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
	if cs.Status != "draft" {
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
	latestReq, latestIncludedPaths, err := activeSliceRequirementsAndIncludedPathsTx(ctx, tx, cs.SliceID)
	if err != nil {
		return nil, err
	}
	for _, p := range patchset.ChangedPaths {
		if !pathInAnyPrefix(latestIncludedPaths, p) {
			return nil, blockSubmitTx(ctx, tx, cs.ID, fmt.Sprintf("changed path %s is outside latest slice definition, refresh the changeset", p))
		}
	}
	nonMechanicalAuthors, err := nonMechanicalPatchsetAuthorsTx(ctx, tx, cs.ID)
	if err != nil {
		return nil, err
	}
	approvalSubjectIDs, err := approvalSubjectsTx(ctx, tx, cs.ID, patchset.Id)
	if err != nil {
		return nil, err
	}
	excludedIDs := append([]string{cs.Author}, nonMechanicalAuthors...)
	allSubjectIDs := make([]string, 0, len(excludedIDs)+len(approvalSubjectIDs))
	allSubjectIDs = append(allSubjectIDs, excludedIDs...)
	allSubjectIDs = append(allSubjectIDs, approvalSubjectIDs...)
	subjectsMap, err := querySubjectsByIDs(ctx, tx, allSubjectIDs)
	if err != nil {
		return nil, err
	}
	excludedAuthors := resolveSubjectList(subjectsMap, excludedIDs)
	approvers := resolveSubjectList(subjectsMap, approvalSubjectIDs)
	checkStatuses, err := checkStatusesTx(ctx, tx, cs.ID, patchset.Id)
	if err != nil {
		return nil, err
	}
	mergeCheckStatuses(checkStatuses, extraCheckStatuses)
	var approverGroupMembers map[string]bool
	if latestReq.GetApprovers() != "" && !bypassRequirements {
		approverGroupMembers, err = groupMemberSubjectIDsTx(ctx, tx, latestReq.GetApprovers())
		if err != nil {
			return nil, err
		}
	}
	if reason := storage.EvaluateSubmitRequirementsWithGroup(latestReq, excludedAuthors, approvers, checkStatuses, approverGroupMembers); reason != "" && !bypassRequirements {
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
		return nil, blockSubmitTx(ctx, tx, cs.ID, storage.StaleBaseReason)
	}
	if err := applyPathHeadEditsTx(ctx, tx, cs.ID, patchset.Id, patchset.FileEdits); err != nil {
		return nil, err
	}
	pendingID, err := objectid.RandomID("pub")
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		insert into pending_publish(id, changeset_id, patchset_id, target_ref, base_ref_commit_id, status, created_at, updated_at)
		values ($1, $2, $3, $4, $5, 'pending', now(), now())
		on conflict (changeset_id) do update
		set id = excluded.id,
		    sequence = nextval(pg_get_serial_sequence('pending_publish', 'sequence')),
		    patchset_id = excluded.patchset_id,
		    target_ref = excluded.target_ref,
		    base_ref_commit_id = excluded.base_ref_commit_id,
		    status = 'pending',
		    commit_id = null,
		    error = null,
		    created_at = now(),
		    updated_at = now(),
		    published_at = null
		where pending_publish.status = 'failed'
	`, pendingID, cs.ID, patchset.Id, cs.TargetRef, currentCommitID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrConflict
	}
	submitAttr := resolveAttributionQuery(ctx, tx, "")
	_, err = tx.ExecContext(ctx, `
		update changesets
		set status = 'pending_publish',
		    submit_blocked_reason = '',
		    submitted_by_subject_id = $2,
		    submitted_controller_subject_id = $3,
		    submitted_key_id = $4,
		    submitted_daemon_id = $5,
		    submitted_conversation_id = $6,
		    updated_at = now()
		where id = $1
	`, cs.ID, submitAttr.ActingSubjectID, submitAttr.ControllerSubjectID, submitAttr.KeyID, submitAttr.DaemonID, submitAttr.ConversationID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.SubmitChangesetResponse{
		TargetRef:        cs.TargetRef,
		Status:           "pending_publish",
		PendingPublishId: pendingID,
		Attribution:      submitAttr.ToProto(),
	}, nil
}
