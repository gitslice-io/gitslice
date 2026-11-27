package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

func (s *SliceStore) CreateTag(ctx context.Context, tag storage.SliceTag) (*storage.SliceTag, bool, error) {
	attr := tag.Attribution.Normalize()
	if attr.ActingSubjectID == "" {
		attr = resolveAttributionQuery(ctx, s.db, tag.CreatedBy)
	}
	res, err := s.db.ExecContext(ctx, `
		insert into slice_tags(
			slice_id, name, commit_id, definition_version, message, created_by, created_at,
			acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
		)
		values ($1, $2, $3, $4, $5, $6, now(), $7, $8, $9, $10, $11)
		on conflict (slice_id, name) do nothing
	`, tag.SliceID, tag.Name, tag.CommitID, tag.DefinitionVersion, tag.Message, tag.CreatedBy,
		attr.ActingSubjectID, attr.ControllerSubjectID, attr.KeyID, attr.DaemonID, attr.ConversationID)
	if err != nil {
		return nil, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	stored, err := s.getTag(ctx, tag.SliceID, tag.Name)
	if err != nil {
		return nil, false, err
	}
	if affected == 0 && stored.CommitID != tag.CommitID {
		return stored, false, fmt.Errorf("%w: tag %q already names commit %s", ErrConflict, tag.Name, stored.CommitID)
	}
	return stored, affected > 0, nil
}

func (s *SliceStore) ListTags(ctx context.Context, sliceID string) ([]storage.SliceTag, error) {
	rows, err := s.db.QueryContext(ctx, `
		select slice_id, name, commit_id, definition_version, message, created_by, created_at,
		       acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
		from slice_tags
		where slice_id = $1
		order by created_at desc, name
	`, sliceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.SliceTag
	for rows.Next() {
		tag, err := scanSliceTag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func (s *SliceStore) ListAccountTags(ctx context.Context, account string, publicOnly bool, sharedWith string) ([]storage.AccountSliceTag, error) {
	account, err := storage.NormalizeSlug(account, "account")
	if err != nil {
		return nil, err
	}
	sharedWith = strings.TrimSpace(sharedWith)
	rows, err := s.db.QueryContext(ctx, `
		select t.slice_id, s.slug, s.visibility,
		       t.name, t.commit_id, t.definition_version, t.message, t.created_by, t.created_at,
		       t.acting_subject_id, t.controller_subject_id, t.key_id, t.daemon_id, t.conversation_id
		from slice_tags t
		join slices s on s.id = t.slice_id
		join accounts a on a.id = s.account_id
		where a.slug = $1
		  and s.deleted_at is null
		  and (not $2 or s.visibility = 'public' or ($3 <> '' and (
			exists (select 1 from slice_members sm where sm.slice_id = s.id and sm.subject_id = $3)
			or exists (
				select 1
				from slice_group_members sgm
				join account_group_members agm on agm.group_id = sgm.group_id
				where sgm.slice_id = s.id and agm.subject_id = $3
			)
		  )))
		order by t.created_at desc, t.name, s.slug
	`, account, publicOnly, sharedWith)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.AccountSliceTag
	for rows.Next() {
		tag, err := scanAccountSliceTag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func scanAccountSliceTag(row scanner) (storage.AccountSliceTag, error) {
	var out storage.AccountSliceTag
	var createdAt time.Time
	if err := row.Scan(
		&out.SliceID, &out.SliceSlug, &out.Visibility,
		&out.Name, &out.CommitID, &out.DefinitionVersion, &out.Message, &out.CreatedBy, &createdAt,
		&out.Attribution.ActingSubjectID, &out.Attribution.ControllerSubjectID, &out.Attribution.KeyID, &out.Attribution.DaemonID, &out.Attribution.ConversationID,
	); err != nil {
		return out, err
	}
	if out.Attribution.ActingSubjectID == "" {
		out.Attribution.ActingSubjectID = out.CreatedBy
	}
	out.Attribution = out.Attribution.Normalize()
	out.CreatedAt = formatTime(createdAt)
	return out, nil
}

func (s *SliceStore) getTag(ctx context.Context, sliceID, name string) (*storage.SliceTag, error) {
	row := s.db.QueryRowContext(ctx, `
		select slice_id, name, commit_id, definition_version, message, created_by, created_at,
		       acting_subject_id, controller_subject_id, key_id, daemon_id, conversation_id
		from slice_tags
		where slice_id = $1 and name = $2
	`, sliceID, name)
	tag, err := scanSliceTag(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &tag, nil
}

func scanSliceTag(row scanner) (storage.SliceTag, error) {
	var tag storage.SliceTag
	var createdAt time.Time
	if err := row.Scan(
		&tag.SliceID, &tag.Name, &tag.CommitID, &tag.DefinitionVersion, &tag.Message, &tag.CreatedBy, &createdAt,
		&tag.Attribution.ActingSubjectID, &tag.Attribution.ControllerSubjectID, &tag.Attribution.KeyID, &tag.Attribution.DaemonID, &tag.Attribution.ConversationID,
	); err != nil {
		return tag, err
	}
	if tag.Attribution.ActingSubjectID == "" {
		tag.Attribution.ActingSubjectID = tag.CreatedBy
	}
	tag.Attribution = tag.Attribution.Normalize()
	tag.CreatedAt = formatTime(createdAt)
	return tag, nil
}
