package postgres

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"gitslice.io/gitslice/internal/intents"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

func (s *ChangesetStore) CreatePathIntent(ctx context.Context, intent storage.PathIntent) (*storage.PathIntent, error) {
	if strings.TrimSpace(intent.SliceID) == "" || strings.TrimSpace(intent.SubjectID) == "" || strings.TrimSpace(intent.Pattern) == "" {
		return nil, ErrInvalid
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = now
	} else {
		intent.CreatedAt = intent.CreatedAt.UTC().Truncate(time.Microsecond)
	}
	intent.ExpiresAt = intent.ExpiresAt.UTC().Truncate(time.Microsecond)
	if intent.ID == "" {
		id, err := objectid.RandomID("int")
		if err != nil {
			return nil, err
		}
		intent.ID = id
	}

	var out storage.PathIntent
	err := s.db.QueryRowContext(ctx, `
		insert into path_intents(
			id, slice_id, account_slug, slice_slug, subject_id, pattern, message, changeset_id, expires_at, created_at
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		returning id, slice_id, account_slug, slice_slug, subject_id, pattern, message, coalesce(changeset_id, ''), expires_at, created_at
	`,
		intent.ID,
		intent.SliceID,
		intent.Account,
		intent.Slice,
		intent.SubjectID,
		intent.Pattern,
		intent.Message,
		nullString(strings.TrimSpace(intent.ChangesetID)),
		intent.ExpiresAt,
		intent.CreatedAt,
	).Scan(
		&out.ID,
		&out.SliceID,
		&out.Account,
		&out.Slice,
		&out.SubjectID,
		&out.Pattern,
		&out.Message,
		&out.ChangesetID,
		&out.ExpiresAt,
		&out.CreatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	out.ExpiresAt = out.ExpiresAt.UTC()
	out.CreatedAt = out.CreatedAt.UTC()
	return &out, nil
}

func (s *ChangesetStore) GetPathIntent(ctx context.Context, intentID string) (*storage.PathIntent, error) {
	var out storage.PathIntent
	err := s.db.QueryRowContext(ctx, `
		select id, slice_id, account_slug, slice_slug, subject_id, pattern, message, coalesce(changeset_id, ''), expires_at, created_at
		from path_intents
		where id = $1
	`, strings.TrimSpace(intentID)).Scan(
		&out.ID,
		&out.SliceID,
		&out.Account,
		&out.Slice,
		&out.SubjectID,
		&out.Pattern,
		&out.Message,
		&out.ChangesetID,
		&out.ExpiresAt,
		&out.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out.ExpiresAt = out.ExpiresAt.UTC()
	out.CreatedAt = out.CreatedAt.UTC()
	return &out, nil
}

func (s *ChangesetStore) ListPathIntents(ctx context.Context, sliceID string, now time.Time) ([]storage.PathIntent, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, slice_id, account_slug, slice_slug, subject_id, pattern, message, coalesce(changeset_id, ''), expires_at, created_at
		from path_intents
		where slice_id = $1
		  and expires_at > $2
		order by created_at asc, id asc
	`, sliceID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []storage.PathIntent
	for rows.Next() {
		var item storage.PathIntent
		if err := rows.Scan(
			&item.ID,
			&item.SliceID,
			&item.Account,
			&item.Slice,
			&item.SubjectID,
			&item.Pattern,
			&item.Message,
			&item.ChangesetID,
			&item.ExpiresAt,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.ExpiresAt = item.ExpiresAt.UTC()
		item.CreatedAt = item.CreatedAt.UTC()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ChangesetStore) DeletePathIntent(ctx context.Context, intentID string) error {
	res, err := s.db.ExecContext(ctx, `
		delete from path_intents
		where id = $1
	`, strings.TrimSpace(intentID))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *ChangesetStore) LinkPathIntentsToChangeset(ctx context.Context, intentIDs []string, subjectID, sliceID, changesetID string) error {
	if len(intentIDs) == 0 || strings.TrimSpace(changesetID) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		update path_intents
		set changeset_id = $1
		where id = any($2)
		  and subject_id = $3
		  and slice_id = $4
		  and changeset_id is null
		  and expires_at > now()
	`, changesetID, intentIDs, subjectID, sliceID)
	return err
}

func clearSubmittedPathIntentsTx(ctx context.Context, tx *sql.Tx, updates []submittedChangesetUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	csIDs := make([]string, 0, len(updates))
	for _, u := range updates {
		csIDs = append(csIDs, u.ChangesetID)
	}
	if _, err := tx.ExecContext(ctx, `
		delete from path_intents
		where changeset_id = any($1)
	`, csIDs); err != nil {
		return err
	}

	for _, u := range updates {
		if u.AuthoringSliceID == "" || u.Author == "" {
			continue
		}
		mentioned := intents.ExtractMentionedIntentIDs(u.Title, u.Description)
		if len(mentioned) == 0 && len(u.ChangedPaths) == 0 {
			continue
		}
		rows, err := tx.QueryContext(ctx, `
			select id, pattern
			from path_intents
			where slice_id = $1
			  and subject_id = $2
		`, u.AuthoringSliceID, u.Author)
		if err != nil {
			return err
		}
		var toDelete []string
		for rows.Next() {
			var id, pattern string
			if err := rows.Scan(&id, &pattern); err != nil {
				_ = rows.Close()
				return err
			}
			if slices.Contains(mentioned, id) || len(intents.OverlappingPaths(pattern, u.ChangedPaths)) > 0 {
				toDelete = append(toDelete, id)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(toDelete) > 0 {
			if _, err := tx.ExecContext(ctx, `
				delete from path_intents
				where id = any($1)
			`, toDelete); err != nil {
				return err
			}
		}
	}
	return nil
}

func clearAbandonedPathIntentsTx(ctx context.Context, tx *sql.Tx, changesetID, sliceID, authorSubjectID, title, description string) error {
	if _, err := tx.ExecContext(ctx, `
		delete from path_intents
		where changeset_id = $1
	`, changesetID); err != nil {
		return err
	}
	mentioned := intents.ExtractMentionedIntentIDs(title, description)
	if len(mentioned) > 0 && sliceID != "" && authorSubjectID != "" {
		if _, err := tx.ExecContext(ctx, `
			delete from path_intents
			where slice_id = $1
			  and subject_id = $2
			  and id = any($3)
		`, sliceID, authorSubjectID, mentioned); err != nil {
			return err
		}
	}
	return nil
}
