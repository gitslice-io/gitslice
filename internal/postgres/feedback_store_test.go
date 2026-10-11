package postgres

import (
	"errors"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

func TestFeedbackStore(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	feedback := store.Feedback()

	first, err := feedback.CreateFeedback(ctx, storage.Feedback{
		SubjectID: "user_alice", Account: "alice", Body: "upload ran silent", Category: "bug",
		Context: map[string]string{"gs_version": "v0.4.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Status != storage.FeedbackOpen || first.CreatedAt.IsZero() || first.Context["gs_version"] != "v0.4.3" || !first.ResolvedAt.IsZero() {
		t.Fatalf("created = %+v", first)
	}
	second, err := feedback.CreateFeedback(ctx, storage.Feedback{SubjectID: "user_bob", Body: "add completions", Category: "idea"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := feedback.CreateFeedback(ctx, storage.Feedback{SubjectID: "user_alice", Body: "doctor", Category: "idea"})
	if err != nil {
		t.Fatal(err)
	}

	all, err := feedback.ListFeedback(ctx, storage.FeedbackFilter{})
	if err != nil || len(all) != 3 || all[0].ID != third.ID || all[2].ID != first.ID {
		t.Fatalf("all = %+v, %v", all, err)
	}
	alices, err := feedback.ListFeedback(ctx, storage.FeedbackFilter{SubjectID: "user_alice"})
	if err != nil || len(alices) != 2 {
		t.Fatalf("alice's = %+v, %v", alices, err)
	}
	page, err := feedback.ListFeedback(ctx, storage.FeedbackFilter{Limit: 1, BeforeCreatedAt: all[0].CreatedAt, BeforeID: all[0].ID})
	if err != nil || len(page) != 1 || page[0].ID != second.ID {
		t.Fatalf("after the first = %+v, %v", page, err)
	}
	if n, err := feedback.CountFeedbackSince(ctx, "user_alice", time.Now().Add(-time.Hour)); err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}

	resolved, err := feedback.SetFeedbackStatus(ctx, first.ID, storage.FeedbackResolved, "progress ships in v0.4.4", "slices")
	if err != nil || resolved.Status != storage.FeedbackResolved || resolved.Resolution != "progress ships in v0.4.4" || resolved.ResolvedBy != "slices" || resolved.ResolvedAt.IsZero() {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	reopened, err := feedback.SetFeedbackStatus(ctx, first.ID, storage.FeedbackOpen, "", "")
	if err != nil || reopened.Status != storage.FeedbackOpen || reopened.Resolution != "progress ships in v0.4.4" || reopened.ResolvedBy != "slices" {
		t.Fatalf("reopened = %+v, %v", reopened, err)
	}
	open, err := feedback.ListFeedback(ctx, storage.FeedbackFilter{Status: storage.FeedbackOpen})
	if err != nil || len(open) != 3 {
		t.Fatalf("open = %+v, %v", open, err)
	}
	if _, err := feedback.SetFeedbackStatus(ctx, "fb_missing", storage.FeedbackResolved, "x", "y"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := feedback.GetFeedback(ctx, "fb_missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get missing = %v", err)
	}
}
