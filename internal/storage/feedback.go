package storage

import (
	"context"
	"time"
)

// Feedback states (design/25_feedback.md).
const (
	FeedbackOpen     = "open"
	FeedbackResolved = "resolved"
)

// FeedbackCategories are the kinds of feedback a sender can pick.
var FeedbackCategories = []string{"bug", "idea", "docs", "other"}

// Feedback is what someone signed in told the Gitslice team, and the team's
// answer.
type Feedback struct {
	ID        string
	SubjectID string
	// Account is the sender's own account when they sent it, for display.
	Account    string
	Body       string
	Category   string
	Context    map[string]string
	Status     string
	Resolution string
	ResolvedBy string
	ResolvedAt time.Time
	CreatedAt  time.Time
}

// FeedbackFilter selects feedback to list, newest first.
type FeedbackFilter struct {
	// SubjectID, when set, lists only that subject's feedback.
	SubjectID string
	// Status is open, resolved, or "" for both.
	Status string
	Limit  int
	// Before continues a listing: only feedback older than this one (its
	// created time, then id).
	BeforeCreatedAt time.Time
	BeforeID        string
}

// FeedbackStore keeps feedback.
type FeedbackStore interface {
	// CreateFeedback stores feedback; it gets an id and time if unset.
	CreateFeedback(ctx context.Context, feedback Feedback) (*Feedback, error)
	GetFeedback(ctx context.Context, id string) (*Feedback, error)
	ListFeedback(ctx context.Context, filter FeedbackFilter) ([]Feedback, error)
	// CountFeedbackSince counts a subject's feedback created after since.
	CountFeedbackSince(ctx context.Context, subjectID string, since time.Time) (int, error)
	// SetFeedbackStatus sets the status; a non-empty resolution replaces the
	// answer, recorded as given by resolvedBy now.
	SetFeedbackStatus(ctx context.Context, id, status, resolution, resolvedBy string) (*Feedback, error)
}
