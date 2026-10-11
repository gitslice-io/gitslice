package service

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FeedbackService takes feedback about Gitslice from anyone signed in and
// lets the Gitslice team read and answer it (design/25_feedback.md). The team
// is the server's operators and the owners and admins of Account.
type FeedbackService struct {
	Auth     storage.AuthStore
	Feedback storage.FeedbackStore
	// Account is the account whose owners and admins read and answer
	// feedback (GITSLICE_FEEDBACK_ACCOUNT).
	Account string
	// OperatorSubjects read and answer feedback too.
	OperatorSubjects []string
	// Now is the clock, for tests.
	Now func() time.Time
}

const (
	maxFeedbackBody           = 20000
	maxFeedbackResolution     = 5000
	maxFeedbackContextEntries = 20
	maxFeedbackContextKey     = 64
	maxFeedbackContextValue   = 1000
	// feedbackPerDay bounds how much one sender can send in a day.
	feedbackPerDay       = 30
	defaultFeedbackLimit = 50
	maxFeedbackLimit     = 200
)

func (s *FeedbackService) available() error {
	if s == nil || s.Feedback == nil {
		return status.Error(codes.Unimplemented, "feedback is not available on this server")
	}
	return nil
}

func (s *FeedbackService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *FeedbackService) SubmitFeedback(ctx context.Context, req *corev1.SubmitFeedbackRequest) (*corev1.Feedback, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(req.GetBody())
	if body == "" {
		return nil, status.Error(codes.InvalidArgument, "feedback body is required")
	}
	if n := utf8.RuneCountInString(body); n > maxFeedbackBody {
		return nil, status.Errorf(codes.InvalidArgument, "feedback is %d characters; the limit is %d", n, maxFeedbackBody)
	}
	category := strings.ToLower(strings.TrimSpace(req.GetCategory()))
	if category == "" {
		category = "other"
	}
	if !slices.Contains(storage.FeedbackCategories, category) {
		return nil, status.Errorf(codes.InvalidArgument, "category must be one of %s", strings.Join(storage.FeedbackCategories, ", "))
	}
	feedbackContext, err := feedbackContextValues(req.GetContext())
	if err != nil {
		return nil, err
	}
	sent, err := s.Feedback.CountFeedbackSince(ctx, subjectID, s.now().Add(-24*time.Hour))
	if err != nil {
		return nil, grpcError(err)
	}
	if sent >= feedbackPerDay {
		return nil, status.Errorf(codes.ResourceExhausted, "you have sent %d pieces of feedback in the last day, the most allowed; send more tomorrow, or put several points in one", sent)
	}
	usernames, err := s.Auth.UsernamesForSubjects(ctx, []string{subjectID})
	if err != nil {
		return nil, grpcError(err)
	}
	created, err := s.Feedback.CreateFeedback(ctx, storage.Feedback{
		SubjectID: subjectID,
		Account:   usernames[subjectID],
		Body:      body,
		Category:  category,
		Context:   feedbackContext,
		CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return feedbackToProto(created), nil
}

func feedbackContextValues(in map[string]string) (map[string]string, error) {
	if len(in) > maxFeedbackContextEntries {
		return nil, status.Errorf(codes.InvalidArgument, "context has %d entries; the limit is %d", len(in), maxFeedbackContextEntries)
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, status.Error(codes.InvalidArgument, "context keys must not be empty")
		}
		if utf8.RuneCountInString(key) > maxFeedbackContextKey {
			return nil, status.Errorf(codes.InvalidArgument, "context key %.20q… is longer than %d characters", key, maxFeedbackContextKey)
		}
		if utf8.RuneCountInString(value) > maxFeedbackContextValue {
			return nil, status.Errorf(codes.InvalidArgument, "context value for %q is longer than %d characters", key, maxFeedbackContextValue)
		}
		out[key] = value
	}
	return out, nil
}

// onTeam reports whether the subject reads and answers everyone's feedback.
func (s *FeedbackService) onTeam(ctx context.Context, subjectID string) (bool, error) {
	if slices.Contains(s.OperatorSubjects, subjectID) {
		return true, nil
	}
	if strings.TrimSpace(s.Account) == "" {
		return false, nil
	}
	return canAuthorizeAccount(ctx, s.Auth, subjectID, s.Account, authz.ActionAdmin)
}

func (s *FeedbackService) teamOnly() error {
	if strings.TrimSpace(s.Account) == "" {
		return status.Error(codes.PermissionDenied, "only the server's operators read everyone's feedback")
	}
	return status.Errorf(codes.PermissionDenied, "only the Gitslice team (the server's operators and the owners and admins of %s) reads everyone's feedback", s.Account)
}

func (s *FeedbackService) ListFeedback(ctx context.Context, req *corev1.ListFeedbackRequest) (*corev1.ListFeedbackResponse, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	filter := storage.FeedbackFilter{Limit: defaultFeedbackLimit}
	switch state := strings.ToLower(strings.TrimSpace(req.GetStatus())); state {
	case "", "all":
	case storage.FeedbackOpen, storage.FeedbackResolved:
		filter.Status = state
	default:
		return nil, status.Error(codes.InvalidArgument, "status must be open, resolved or all")
	}
	if limit := int(req.GetLimit()); limit > 0 {
		filter.Limit = min(limit, maxFeedbackLimit)
	}
	if req.GetMine() {
		filter.SubjectID = subjectID
	} else {
		team, err := s.onTeam(ctx, subjectID)
		if err != nil {
			return nil, err
		}
		if !team {
			return nil, s.teamOnly()
		}
	}
	if cursor := strings.TrimSpace(req.GetCursor()); cursor != "" {
		createdAt, id, err := decodeFeedbackCursor(cursor)
		if err != nil {
			return nil, err
		}
		filter.BeforeCreatedAt, filter.BeforeID = createdAt, id
	}
	page := filter.Limit
	filter.Limit = page + 1
	items, err := s.Feedback.ListFeedback(ctx, filter)
	if err != nil {
		return nil, grpcError(err)
	}
	res := &corev1.ListFeedbackResponse{}
	if len(items) > page {
		items = items[:page]
		last := items[len(items)-1]
		res.NextCursor = encodeFeedbackCursor(last.CreatedAt, last.ID)
	}
	for i := range items {
		res.Feedback = append(res.Feedback, feedbackToProto(&items[i]))
	}
	return res, nil
}

func (s *FeedbackService) ResolveFeedback(ctx context.Context, req *corev1.ResolveFeedbackRequest) (*corev1.Feedback, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	team, err := s.onTeam(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	if !team {
		return nil, s.teamOnly()
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "feedback id is required")
	}
	if req.GetReopen() {
		reopened, err := s.Feedback.SetFeedbackStatus(ctx, id, storage.FeedbackOpen, "", "")
		if err != nil {
			return nil, grpcError(err)
		}
		return feedbackToProto(reopened), nil
	}
	resolution := strings.TrimSpace(req.GetResolution())
	if resolution == "" {
		return nil, status.Error(codes.InvalidArgument, "resolution is required: say what changed, or why nothing will")
	}
	if n := utf8.RuneCountInString(resolution); n > maxFeedbackResolution {
		return nil, status.Errorf(codes.InvalidArgument, "resolution is %d characters; the limit is %d", n, maxFeedbackResolution)
	}
	usernames, err := s.Auth.UsernamesForSubjects(ctx, []string{subjectID})
	if err != nil {
		return nil, grpcError(err)
	}
	resolved, err := s.Feedback.SetFeedbackStatus(ctx, id, storage.FeedbackResolved, resolution, firstNonEmptyString(usernames[subjectID], subjectID))
	if err != nil {
		return nil, grpcError(err)
	}
	return feedbackToProto(resolved), nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func encodeFeedbackCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeFeedbackCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err == nil {
		if stamp, id, ok := strings.Cut(string(raw), "|"); ok && id != "" {
			if createdAt, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
				return createdAt, id, nil
			}
		}
	}
	return time.Time{}, "", status.Error(codes.InvalidArgument, "cursor is not one ListFeedback returned")
}

func feedbackToProto(f *storage.Feedback) *corev1.Feedback {
	if f == nil {
		return nil
	}
	out := &corev1.Feedback{
		Id:         f.ID,
		SubjectId:  f.SubjectID,
		Account:    f.Account,
		Body:       f.Body,
		Category:   f.Category,
		Context:    f.Context,
		Status:     f.Status,
		Resolution: f.Resolution,
		ResolvedBy: f.ResolvedBy,
		CreatedAt:  f.CreatedAt.UTC().Format(time.RFC3339),
	}
	if !f.ResolvedAt.IsZero() {
		out.ResolvedAt = f.ResolvedAt.UTC().Format(time.RFC3339)
	}
	return out
}
