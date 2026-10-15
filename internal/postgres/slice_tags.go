package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

func (s *SliceStore) CreateTag(ctx context.Context, tag storage.SliceTag) (*storage.SliceTag, bool, error) {
	res, err := s.db.ExecContext(ctx, `
		insert into slice_tags(slice_id, name, commit_id, definition_version, message, created_by, created_at)
		values ($1, $2, $3, $4, $5, $6, now())
		on conflict (slice_id, name) do nothing
	`, tag.SliceID, tag.Name, tag.CommitID, tag.DefinitionVersion, tag.Message, tag.CreatedBy)
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
		select slice_id, name, commit_id, definition_version, message, created_by, created_at
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

func (s *SliceStore) getTag(ctx context.Context, sliceID, name string) (*storage.SliceTag, error) {
	row := s.db.QueryRowContext(ctx, `
		select slice_id, name, commit_id, definition_version, message, created_by, created_at
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
	if err := row.Scan(&tag.SliceID, &tag.Name, &tag.CommitID, &tag.DefinitionVersion, &tag.Message, &tag.CreatedBy, &createdAt); err != nil {
		return tag, err
	}
	tag.CreatedAt = formatTime(createdAt)
	return tag, nil
}
