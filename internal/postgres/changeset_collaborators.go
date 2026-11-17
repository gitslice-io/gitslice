package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) listCollaborators(ctx context.Context, changesetID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		select subject_id
		from changeset_collaborators
		where changeset_id = $1
		order by created_at asc, subject_id asc
	`, changesetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var subjectID string
		if err := rows.Scan(&subjectID); err != nil {
			return nil, err
		}
		out = append(out, subjectID)
	}
	return out, rows.Err()
}

func (s *ChangesetStore) listCollaboratorsForChangesets(ctx context.Context, ids []string) (map[string][]string, error) {
	out := make(map[string][]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select changeset_id, subject_id
		from changeset_collaborators
		where changeset_id = any($1)
		order by changeset_id asc, created_at asc, subject_id asc
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var csID, subjectID string
		if err := rows.Scan(&csID, &subjectID); err != nil {
			return nil, err
		}
		out[csID] = append(out[csID], subjectID)
	}
	return out, rows.Err()
}

func (s *ChangesetStore) AddCollaborator(ctx context.Context, changesetID, subjectID, addedBy string) (*corev1.Changeset, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	addedBy = strings.TrimSpace(addedBy)
	if subjectID == "" {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var authorSubjectID, status string
	err = tx.QueryRowContext(ctx, `
		select author_subject_id, status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&authorSubjectID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "draft" {
		return nil, fmt.Errorf("%w: collaborators can only be modified while a changeset is open; this one is %s", ErrConflict, status)
	}
	if subjectID != strings.TrimSpace(authorSubjectID) {
		if _, err := tx.ExecContext(ctx, `
			insert into changeset_collaborators(changeset_id, subject_id, added_by)
			values ($1, $2, $3)
			on conflict (changeset_id, subject_id) do nothing
		`, changesetID, subjectID, addedBy); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			update changesets set updated_at = now() where id = $1
		`, changesetID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, changesetID)
}

func (s *ChangesetStore) RemoveCollaborator(ctx context.Context, changesetID, subjectID string) (*corev1.Changeset, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `
		select status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "draft" {
		return nil, fmt.Errorf("%w: collaborators can only be modified while a changeset is open; this one is %s", ErrConflict, status)
	}
	if _, err := tx.ExecContext(ctx, `
		delete from changeset_collaborators
		where changeset_id = $1 and subject_id = $2
	`, changesetID, subjectID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		update changesets set updated_at = now() where id = $1
	`, changesetID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, changesetID)
}

func (s *ChangesetStore) Handoff(ctx context.Context, changesetID, newAuthorSubjectID string) (*corev1.Changeset, error) {
	changesetID, err := s.resolveChangesetSelector(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	newAuthorSubjectID = strings.TrimSpace(newAuthorSubjectID)
	if newAuthorSubjectID == "" {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var authorSubjectID, status string
	err = tx.QueryRowContext(ctx, `
		select author_subject_id, status
		from changesets
		where id = $1
		for update
	`, changesetID).Scan(&authorSubjectID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "draft" {
		return nil, fmt.Errorf("%w: a changeset can only be handed off while it is open; this one is %s", ErrConflict, status)
	}
	if newAuthorSubjectID != strings.TrimSpace(authorSubjectID) {
		if _, err := tx.ExecContext(ctx, `
			update changesets
			set original_author_subject_id = coalesce(original_author_subject_id, author_subject_id),
			    author_subject_id = $2,
			    updated_at = now()
			where id = $1
		`, changesetID, newAuthorSubjectID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			delete from changeset_collaborators
			where changeset_id = $1 and subject_id = $2
		`, changesetID, newAuthorSubjectID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, changesetID)
}
