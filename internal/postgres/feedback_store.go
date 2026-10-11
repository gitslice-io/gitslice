package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// FeedbackStore keeps feedback about Gitslice (design/25_feedback.md).
type FeedbackStore struct {
	db *sql.DB
}

const feedbackColumns = `id, subject_id, account, body, category, context, status, resolution, resolved_by, resolved_at, created_at`

func scanFeedback(row rowScanner) (*storage.Feedback, error) {
	var f storage.Feedback
	var contextJSON []byte
	var resolvedAt sql.NullTime
	if err := row.Scan(&f.ID, &f.SubjectID, &f.Account, &f.Body, &f.Category, &contextJSON, &f.Status, &f.Resolution, &f.ResolvedBy, &resolvedAt, &f.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(contextJSON, &f.Context); err != nil {
		return nil, err
	}
	if resolvedAt.Valid {
		f.ResolvedAt = resolvedAt.Time
	}
	return &f, nil
}

func (s *FeedbackStore) CreateFeedback(ctx context.Context, feedback storage.Feedback) (*storage.Feedback, error) {
	if feedback.ID == "" {
		id, err := objectid.RandomID("fb")
		if err != nil {
			return nil, err
		}
		feedback.ID = id
	}
	if feedback.Status == "" {
		feedback.Status = storage.FeedbackOpen
	}
	if feedback.Context == nil {
		feedback.Context = map[string]string{}
	}
	contextJSON, err := json.Marshal(feedback.Context)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		insert into feedback(id, subject_id, account, body, category, context, status)
		values ($1, $2, $3, $4, $5, $6, $7)
		returning `+feedbackColumns,
		feedback.ID, feedback.SubjectID, feedback.Account, feedback.Body, feedback.Category, contextJSON, feedback.Status)
	return scanFeedback(row)
}

func (s *FeedbackStore) GetFeedback(ctx context.Context, id string) (*storage.Feedback, error) {
	f, err := scanFeedback(s.db.QueryRowContext(ctx, `select `+feedbackColumns+` from feedback where id = $1`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

func (s *FeedbackStore) ListFeedback(ctx context.Context, filter storage.FeedbackFilter) ([]storage.Feedback, error) {
	var where []string
	var args []any
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.SubjectID != "" {
		where = append(where, "subject_id = "+arg(filter.SubjectID))
	}
	if filter.Status != "" {
		where = append(where, "status = "+arg(filter.Status))
	}
	if !filter.BeforeCreatedAt.IsZero() {
		where = append(where, fmt.Sprintf("(created_at, id) < (%s, %s)", arg(filter.BeforeCreatedAt), arg(filter.BeforeID)))
	}
	query := `select ` + feedbackColumns + ` from feedback`
	if len(where) > 0 {
		query += ` where ` + strings.Join(where, " and ")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	query += ` order by created_at desc, id desc limit ` + arg(limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *FeedbackStore) CountFeedbackSince(ctx context.Context, subjectID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from feedback where subject_id = $1 and created_at > $2`, subjectID, since).Scan(&n)
	return n, err
}

func (s *FeedbackStore) SetFeedbackStatus(ctx context.Context, id, status, resolution, resolvedBy string) (*storage.Feedback, error) {
	f, err := scanFeedback(s.db.QueryRowContext(ctx, `
		update feedback set
			status = $2,
			resolution = case when $3 = '' then resolution else $3 end,
			resolved_by = case when $3 = '' then resolved_by else $4 end,
			resolved_at = case when $3 = '' then resolved_at else now() end
		where id = $1
		returning `+feedbackColumns,
		strings.TrimSpace(id), status, resolution, resolvedBy))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}
