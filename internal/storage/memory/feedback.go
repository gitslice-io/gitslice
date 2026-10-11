package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// FeedbackStore is the in-memory storage.FeedbackStore.
type FeedbackStore struct {
	b *backend
}

func cloneFeedback(f storage.Feedback) storage.Feedback {
	context := make(map[string]string, len(f.Context))
	for key, value := range f.Context {
		context[key] = value
	}
	f.Context = context
	return f
}

func (s *FeedbackStore) CreateFeedback(ctx context.Context, feedback storage.Feedback) (*storage.Feedback, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
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
	if feedback.CreatedAt.IsZero() {
		feedback.CreatedAt = time.Now().UTC()
	}
	s.b.feedback[feedback.ID] = cloneFeedback(feedback)
	out := cloneFeedback(feedback)
	return &out, nil
}

func (s *FeedbackStore) GetFeedback(ctx context.Context, id string) (*storage.Feedback, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	f, ok := s.b.feedback[strings.TrimSpace(id)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	out := cloneFeedback(f)
	return &out, nil
}

func (s *FeedbackStore) ListFeedback(ctx context.Context, filter storage.FeedbackFilter) ([]storage.Feedback, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.Feedback
	for _, f := range s.b.feedback {
		if filter.SubjectID != "" && f.SubjectID != filter.SubjectID {
			continue
		}
		if filter.Status != "" && f.Status != filter.Status {
			continue
		}
		if !filter.BeforeCreatedAt.IsZero() && !feedbackBefore(f, filter.BeforeCreatedAt, filter.BeforeID) {
			continue
		}
		out = append(out, cloneFeedback(f))
	}
	sort.Slice(out, func(i, j int) bool {
		return feedbackBefore(out[j], out[i].CreatedAt, out[i].ID)
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// feedbackBefore reports whether f sorts after (createdAt, id) newest first.
func feedbackBefore(f storage.Feedback, createdAt time.Time, id string) bool {
	if !f.CreatedAt.Equal(createdAt) {
		return f.CreatedAt.Before(createdAt)
	}
	return f.ID < id
}

func (s *FeedbackStore) CountFeedbackSince(ctx context.Context, subjectID string, since time.Time) (int, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	n := 0
	for _, f := range s.b.feedback {
		if f.SubjectID == subjectID && f.CreatedAt.After(since) {
			n++
		}
	}
	return n, nil
}

func (s *FeedbackStore) SetFeedbackStatus(ctx context.Context, id, status, resolution, resolvedBy string) (*storage.Feedback, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	f, ok := s.b.feedback[strings.TrimSpace(id)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	f.Status = status
	if resolution != "" {
		f.Resolution = resolution
		f.ResolvedBy = resolvedBy
		f.ResolvedAt = time.Now().UTC()
	}
	s.b.feedback[f.ID] = f
	out := cloneFeedback(f)
	return &out, nil
}
