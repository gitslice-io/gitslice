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
	return inTx(ctx, s.db, func(tx *sql.Tx) (*corev1.ApproveChangesetResponse, error) {
		var patchsetID, status string
		err := tx.QueryRowContext(ctx, `
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
		if status != "draft" {
			return nil, ErrConflict
		}
		attr := resolveAttributionQuery(ctx, tx, subjectID)
		_, err = tx.ExecContext(ctx, `
			insert into approvals(
				changeset_id, patchset_id, subject_id, created_at,
				acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
			)
			values ($1, $2, $3, now(), $4, $5, $6, $7, $8)
			on conflict do nothing
		`, changesetID, patchsetID, subjectID, attr.ActingSubjectID, attr.ControllerSubjectID, attr.KeyID, attr.DaemonID, attr.ConversationID)
		if err != nil {
			return nil, err
		}
		storedApproval, err := scanChangesetApproval(tx.QueryRowContext(ctx, `
			select patchset_id, subject_id, created_at,
			       acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
			from approvals
			where changeset_id = $1 and patchset_id = $2 and subject_id = $3
		`, changesetID, patchsetID, subjectID))
		if err != nil {
			return nil, err
		}
		if err := clearRequirementBlockTx(ctx, tx, changesetID, patchsetID); err != nil {
			return nil, err
		}
		return &corev1.ApproveChangesetResponse{
			ChangesetId: changesetID,
			PatchsetId:  patchsetID,
			SubjectId:   subjectID,
			Attribution: storedApproval.GetAttribution(),
		}, nil
	})
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
	return inTx(ctx, s.db, func(tx *sql.Tx) (*corev1.ReportCheckResultResponse, error) {
		var patchsetID, changesetStatus string
		err := tx.QueryRowContext(ctx, `
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
		if changesetStatus != "draft" {
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
		return &corev1.ReportCheckResultResponse{ChangesetId: changesetID, PatchsetId: patchsetID, CheckName: checkName, Status: resultStatus}, nil
	})
}

func submitRequirementsForSliceTx(ctx context.Context, q dbtx, sliceID string) (*corev1.SubmitRequirements, error) {
	req, _, err := submitRequirementsAndIncludedPathsForSliceTx(ctx, q, sliceID)
	return req, err
}

func submitRequirementsAndIncludedPathsForSliceTx(ctx context.Context, q dbtx, sliceID string) (*corev1.SubmitRequirements, []string, error) {
	req, included, _, _, err := sliceSubmitMetadataTx(ctx, q, sliceID)
	return req, included, err
}

func activeSliceRequirementsAndIncludedPathsTx(ctx context.Context, q dbtx, sliceID string) (*corev1.SubmitRequirements, []string, error) {
	req, included, archived, deleted, err := sliceSubmitMetadataTx(ctx, q, sliceID)
	if err != nil {
		return nil, nil, err
	}
	if err := storage.ValidateSubmitSliceState(archived, deleted); err != nil {
		return nil, nil, err
	}
	return req, included, nil
}

func sliceSubmitMetadataTx(ctx context.Context, q dbtx, sliceID string) (*corev1.SubmitRequirements, []string, bool, bool, error) {
	var requiredApprovals, requiredHumanApprovals int64
	var included, requiredChecks []string
	var approversGroup string
	var archivedAt, deletedAt sql.NullTime
	err := q.QueryRowContext(ctx, `
		select included_paths, required_approvals, required_human_approvals, required_checks, approvers_group, archived_at, deleted_at
		from slices
		where id = $1
	`, sliceID).Scan(jsonCol(&included), &requiredApprovals, &requiredHumanApprovals, jsonCol(&requiredChecks), &approversGroup, &archivedAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, false, ErrNotFound
	}
	if err != nil {
		return nil, nil, false, false, err
	}
	return &corev1.SubmitRequirements{
		RequiredApprovals:      int32(requiredApprovals),
		RequiredHumanApprovals: int32(requiredHumanApprovals),
		RequiredChecks:         requiredChecks,
		Approvers:              approversGroup,
		SourceSliceDefinitionHash: storage.SubmitRequirementsHashWithApprovers(
			included,
			int32(requiredApprovals),
			int32(requiredHumanApprovals),
			requiredChecks,
			approversGroup,
		),
	}, included, archivedAt.Valid, deletedAt.Valid, nil
}

func nonMechanicalPatchsetAuthorsTx(ctx context.Context, q dbtx, changesetID string) ([]string, error) {
	return querySlice(ctx, q, scanString, `
		select distinct author_subject_id
		from patchsets
		where changeset_id = $1
		  and coalesce(kind, '') <> $2
		order by author_subject_id
	`, changesetID, storage.SubmitMergeKind)
}

func querySubjectsByIDs(ctx context.Context, q dbtx, subjectIDs []string) (map[string]storage.Subject, error) {
	ids := make([]string, 0, len(subjectIDs))
	seen := make(map[string]struct{}, len(subjectIDs))
	for _, id := range subjectIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	out := make(map[string]storage.Subject, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	list, err := querySlice(ctx, q, func(rows *sql.Rows) (storage.Subject, error) {
		var subj storage.Subject
		err := rows.Scan(&subj.ID, &subj.Kind, &subj.ControllerSubjectID, &subj.DisplayName)
		return subj, err
	}, `
		select id, kind, coalesce(controller_subject_id, ''), display_name
		from subjects
		where id = any($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	for _, subj := range list {
		out[subj.ID] = subj
	}
	return out, nil
}

func resolveSubjectList(byID map[string]storage.Subject, subjectIDs []string) []storage.Subject {
	out := make([]storage.Subject, 0, len(subjectIDs))
	for _, id := range subjectIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if subj, ok := byID[id]; ok {
			out = append(out, subj)
		} else {
			out = append(out, storage.Subject{ID: id})
		}
	}
	return out
}

func approvalSubjectsTx(ctx context.Context, q dbtx, changesetID, patchsetID string) ([]string, error) {
	return querySlice(ctx, q, scanString, `
		select subject_id
		from approvals
		where changeset_id = $1 and patchset_id = $2
		order by subject_id
	`, changesetID, patchsetID)
}

func checkStatusesTx(ctx context.Context, q dbtx, changesetID, patchsetID string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `
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
// or approvals (storage.SubmitBlockCode.IsRequirementBlock) when the current
// patchset gets a new check result or approval: the reason described the last
// submit attempt, whose inputs have changed, and the next submit evaluates the
// requirements again. Without this, gs cs status kept reporting a check as
// having no result after it had passed.
func clearRequirementBlockTx(ctx context.Context, tx *sql.Tx, changesetID, patchsetID string) error {
	_, err := tx.ExecContext(ctx, `
		update changesets
		set submit_blocked_reason = '',
		    submit_blocked_code = '',
		    updated_at = now()
		where id = $1
		  and current_patchset_id = $2
		  and submit_blocked_code = any($3)
	`, changesetID, patchsetID, storage.RequirementBlockCodes())
	return err
}

// RecordSubmitBlockedReason persists a requirement failure (such as path owner
// or .gitslice approvals) evaluated by the service layer onto a draft changeset.
func (s *ChangesetStore) RecordSubmitBlockedReason(ctx context.Context, changesetID, expectedCurrentPatchsetID, reason string) error {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return err
	}
	code := storage.ClassifySubmitBlockedReason(reason)
	_, err = s.db.ExecContext(ctx, `
		update changesets
		set submit_blocked_reason = $3,
		    submit_blocked_code = $4,
		    updated_at = now()
		where id = $1
		  and status = 'draft'
		  and coalesce(current_patchset_id, '') = $2
	`, changesetID, strings.TrimSpace(expectedCurrentPatchsetID), reason, string(code))
	return err
}

func blockSubmitTx(ctx context.Context, tx *sql.Tx, changesetID string, code storage.SubmitBlockCode, reason string) error {
	if code == storage.SubmitBlockCodeNone {
		code = storage.ClassifySubmitBlockedReason(reason)
	}
	_, err := tx.ExecContext(ctx, `
		update changesets
		set submit_blocked_reason = $2,
		    submit_blocked_code = $3,
		    updated_at = now()
		where id = $1
	`, changesetID, reason, string(code))
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return storage.NewSubmitBlockedError(code, reason)
}

// ApprovalSubjects lists who approved a patchset.
func (s *ChangesetStore) ApprovalSubjects(ctx context.Context, changesetID, patchsetID string) ([]string, error) {
	return approvalSubjectsTx(ctx, s.db, changesetID, patchsetID)
}
