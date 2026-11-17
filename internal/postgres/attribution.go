package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func resolveAttributionQuery(ctx context.Context, q publishQueryer, fallbackSubjectID string) storage.ActorAttribution {
	attr := storage.ActorAttributionFrom(ctx, fallbackSubjectID)
	if attr.ActingSubjectID != "" && attr.ControllerSubjectID == "" && q != nil {
		var ctrl string
		err := q.QueryRowContext(ctx, `
			select coalesce(controller_subject_id, '')
			from subjects
			where id = $1
		`, attr.ActingSubjectID).Scan(&ctrl)
		if err == nil {
			ctrl = strings.TrimSpace(ctrl)
			if ctrl != "" && ctrl != attr.ActingSubjectID {
				attr.ControllerSubjectID = ctrl
			}
		}
	}
	return attr.Normalize()
}

func (s *ChangesetStore) listApprovals(ctx context.Context, changesetID string) ([]*corev1.ChangesetApproval, error) {
	rows, err := s.db.QueryContext(ctx, `
		select patchset_id, subject_id, created_at,
		       acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
		from approvals
		where changeset_id = $1
		order by created_at asc, subject_id asc
	`, changesetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*corev1.ChangesetApproval
	for rows.Next() {
		item, err := scanChangesetApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *ChangesetStore) listApprovalsForChangesets(ctx context.Context, changesetIDs []string) (map[string][]*corev1.ChangesetApproval, error) {
	out := make(map[string][]*corev1.ChangesetApproval, len(changesetIDs))
	if len(changesetIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select changeset_id, patchset_id, subject_id, created_at,
		       acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
		from approvals
		where changeset_id = any($1)
		order by changeset_id asc, created_at asc, subject_id asc
	`, changesetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var csID, patchsetID, subjectID string
		var createdAt time.Time
		var attr storage.ActorAttribution
		if err := rows.Scan(
			&csID,
			&patchsetID,
			&subjectID,
			&createdAt,
			&attr.ActingSubjectID,
			&attr.ControllerSubjectID,
			&attr.KeyID,
			&attr.DaemonID,
			&attr.ConversationID,
		); err != nil {
			return nil, err
		}
		if attr.ActingSubjectID == "" {
			attr.ActingSubjectID = subjectID
		}
		out[csID] = append(out[csID], &corev1.ChangesetApproval{
			PatchsetId:  patchsetID,
			SubjectId:   subjectID,
			CreatedAt:   formatTime(createdAt),
			Attribution: attr.ToProto(),
		})
	}
	return out, rows.Err()
}

func scanChangesetApproval(row scanner) (*corev1.ChangesetApproval, error) {
	var patchsetID, subjectID string
	var createdAt time.Time
	var attr storage.ActorAttribution
	if err := row.Scan(
		&patchsetID,
		&subjectID,
		&createdAt,
		&attr.ActingSubjectID,
		&attr.ControllerSubjectID,
		&attr.KeyID,
		&attr.DaemonID,
		&attr.ConversationID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if attr.ActingSubjectID == "" {
		attr.ActingSubjectID = subjectID
	}
	return &corev1.ChangesetApproval{
		PatchsetId:  patchsetID,
		SubjectId:   subjectID,
		CreatedAt:   formatTime(createdAt),
		Attribution: attr.ToProto(),
	}, nil
}
