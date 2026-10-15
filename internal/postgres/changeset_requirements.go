package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) Approve(ctx context.Context, changesetID, subjectID string) (*corev1.ApproveChangesetResponse, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var patchsetID, status string
	err = tx.QueryRowContext(ctx, `
		select coalesce(current_patchset_id, ''), status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&patchsetID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if patchsetID == "" {
		return nil, fmt.Errorf("%w: changeset has no current patchset", ErrConflict)
	}
	if status == "abandoned" || status == "submitted" {
		return nil, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `
		insert into approvals(changeset_id, patchset_id, subject_id, created_at)
		values ($1, $2, $3, now())
		on conflict do nothing
	`, changesetID, patchsetID, subjectID)
	if err != nil {
		return nil, err
	}
	if err := clearRequirementBlockTx(ctx, tx, changesetID, patchsetID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.ApproveChangesetResponse{ChangesetId: changesetID, PatchsetId: patchsetID, SubjectId: subjectID}, nil
}

func (s *ChangesetStore) ReportCheckResult(ctx context.Context, changesetID, subjectID, checkName, resultStatus string) (*corev1.ReportCheckResultResponse, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	checkName = strings.TrimSpace(checkName)
	if subjectID == "" {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if checkName == "" {
		return nil, fmt.Errorf("%w: check name is required", ErrInvalid)
	}
	resultStatus, ok := storage.NormalizeCheckStatus(resultStatus)
	if !ok {
		return nil, fmt.Errorf("%w: check status must be pass or fail", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var patchsetID, changesetStatus string
	err = tx.QueryRowContext(ctx, `
		select coalesce(current_patchset_id, ''), status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&patchsetID, &changesetStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if patchsetID == "" {
		return nil, fmt.Errorf("%w: changeset has no current patchset", ErrConflict)
	}
	if changesetStatus == "abandoned" || changesetStatus == "submitted" {
		return nil, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `
		insert into check_results(changeset_id, patchset_id, check_name, status, reported_by, created_at, updated_at)
		values ($1, $2, $3, $4, $5, now(), now())
		on conflict (changeset_id, patchset_id, check_name) do update
		set status = excluded.status,
		    reported_by = excluded.reported_by,
		    updated_at = now()
	`, changesetID, patchsetID, checkName, resultStatus, subjectID)
	if err != nil {
		return nil, err
	}
	if err := clearRequirementBlockTx(ctx, tx, changesetID, patchsetID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.ReportCheckResultResponse{ChangesetId: changesetID, PatchsetId: patchsetID, CheckName: checkName, Status: resultStatus}, nil
}

func submitRequirementsForSliceTx(ctx context.Context, tx *sql.Tx, sliceID string) (*corev1.SubmitRequirements, error) {
	req, _, err := submitRequirementsAndIncludedPathsForSliceTx(ctx, tx, sliceID)
	return req, err
}

func submitRequirementsAndIncludedPathsForSliceTx(ctx context.Context, tx *sql.Tx, sliceID string) (*corev1.SubmitRequirements, []string, error) {
	var requiredApprovals int64
	var includedJSON, requiredChecksJSON []byte
	err := tx.QueryRowContext(ctx, `
		select included_paths, required_approvals, required_checks
		from slices
		where id = $1
	`, sliceID).Scan(&includedJSON, &requiredApprovals, &requiredChecksJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var included []string
	if err := decodeJSON(includedJSON, &included); err != nil {
		return nil, nil, err
	}
	var requiredChecks []string
	if err := decodeJSON(requiredChecksJSON, &requiredChecks); err != nil {
		return nil, nil, err
	}
	return &corev1.SubmitRequirements{
		RequiredApprovals: int32(requiredApprovals),
		RequiredChecks:    requiredChecks,
		SourceSliceDefinitionHash: storage.SubmitRequirementsHash(
			included,
			int32(requiredApprovals),
			requiredChecks,
		),
	}, included, nil
}

func approvalSubjectsTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		select subject_id
		from approvals
		where changeset_id = $1 and patchset_id = $2
		order by subject_id
	`, changesetID, patchsetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subjects []string
	for rows.Next() {
		var subjectID string
		if err := rows.Scan(&subjectID); err != nil {
			return nil, err
		}
		subjects = append(subjects, subjectID)
	}
	return subjects, rows.Err()
}

func checkStatusesTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `
		select check_name, status
		from check_results
		where changeset_id = $1 and patchset_id = $2
	`, changesetID, patchsetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := map[string]string{}
	for rows.Next() {
		var checkName, status string
		if err := rows.Scan(&checkName, &status); err != nil {
			return nil, err
		}
		statuses[checkName] = status
	}
	return statuses, rows.Err()
}

func mergeCheckStatuses(statuses map[string]string, extra map[string]string) {
	if len(extra) == 0 {
		return
	}
	for checkName, status := range extra {
		checkName = strings.TrimSpace(checkName)
		normalized, ok := storage.NormalizeCheckStatus(status)
		if checkName == "" || !ok {
			continue
		}
		statuses[checkName] = normalized
	}
}

// clearRequirementBlockTx clears a stored submit-blocked reason about checks
// or approvals (storage.IsRequirementBlock) when the current patchset gets a
// new check result or approval: the reason described the last submit
// attempt, whose inputs have changed, and the next submit evaluates the
// requirements again. Without this, gs cs status kept reporting a check as
// having no result after it had passed.
func clearRequirementBlockTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string) error {
	_, err := tx.ExecContext(ctx, `
		update changesets
		set submit_blocked_reason = '',
		    updated_at = now()
		where id = $1
		  and current_patchset_id = $2
		  and (submit_blocked_reason like 'required check %' or submit_blocked_reason like 'required approvals %')
	`, changesetID, patchsetID)
	return err
}

func blockSubmitTx(ctx context.Context, tx *sql.Tx, changesetID, reason string) error {
	_, err := tx.ExecContext(ctx, `
		update changesets
		set submit_blocked_reason = $2,
		    updated_at = now()
		where id = $1
	`, changesetID, reason)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrConflict, reason)
}
