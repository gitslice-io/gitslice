package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newFeedbackService has alice and bob sending feedback, carol an owner of
// the gitslice account, and agent_op an operator.
func newFeedbackService(t *testing.T) (*FeedbackService, *time.Time) {
	t.Helper()
	mem := memory.New()
	mem.AddPersonalAccount("user_alice", "alice")
	mem.AddPersonalAccount("user_bob", "bob")
	mem.AddPersonalAccount("user_carol", "carol")
	mem.AddAccountRole("user_carol", "gitslice", "owner")
	mem.AddAccountRole("user_dave", "gitslice", "reader")
	handlers := New(Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
		Feedback:   mem.Feedback,
	}, mem.Objects, nil)
	svc := handlers.Feedback
	svc.Account = "gitslice"
	svc.OperatorSubjects = []string{"agent_op"}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	return svc, &now
}

func asSubject(subject string) context.Context {
	return authctx.WithSubjectID(context.Background(), subject)
}

func wantFeedbackErr(t *testing.T, err error, code codes.Code, contains string) {
	t.Helper()
	if status.Code(err) != code || !strings.Contains(status.Convert(err).Message(), contains) {
		t.Fatalf("err = %v, want %s containing %q", err, code, contains)
	}
}

func TestFeedbackSubmitAndValidate(t *testing.T) {
	svc, _ := newFeedbackService(t)
	got, err := svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{
		Body:    "  the 1,742-file upload ran silent  ",
		Context: map[string]string{"gs_version": "v0.4.3", "os": "linux"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Id, "fb_") || got.Account != "alice" || got.Body != "the 1,742-file upload ran silent" ||
		got.Category != "other" || got.Status != "open" || got.Context["gs_version"] != "v0.4.3" || got.CreatedAt != "2026-10-11T12:00:00Z" {
		t.Fatalf("submitted = %+v", got)
	}

	_, err = svc.SubmitFeedback(context.Background(), &corev1.SubmitFeedbackRequest{Body: "anonymous"})
	wantFeedbackErr(t, err, codes.Unauthenticated, "")
	_, err = svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: "   "})
	wantFeedbackErr(t, err, codes.InvalidArgument, "body is required")
	_, err = svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: "x", Category: "rant"})
	wantFeedbackErr(t, err, codes.InvalidArgument, "bug, idea, docs, other")
	_, err = svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: strings.Repeat("é", maxFeedbackBody+1)})
	wantFeedbackErr(t, err, codes.InvalidArgument, "the limit is 20000")
	many := map[string]string{}
	for i := 0; i <= maxFeedbackContextEntries; i++ {
		many[strings.Repeat("k", i+1)] = "v"
	}
	_, err = svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: "x", Context: many})
	wantFeedbackErr(t, err, codes.InvalidArgument, "the limit is 20")
	_, err = svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: "x", Context: map[string]string{"cmd": strings.Repeat("a", 1001)}})
	wantFeedbackErr(t, err, codes.InvalidArgument, `"cmd" is longer than 1000`)
	if got, err := svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: "x", Category: " BUG "}); err != nil || got.Category != "bug" {
		t.Fatalf("category = %+v, %v", got, err)
	}
}

func TestFeedbackDailyLimit(t *testing.T) {
	svc, now := newFeedbackService(t)
	for i := 0; i < feedbackPerDay; i++ {
		if _, err := svc.SubmitFeedback(asSubject("user_bob"), &corev1.SubmitFeedbackRequest{Body: "point"}); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Minute)
	}
	_, err := svc.SubmitFeedback(asSubject("user_bob"), &corev1.SubmitFeedbackRequest{Body: "one more"})
	wantFeedbackErr(t, err, codes.ResourceExhausted, "in the last day")
	// Others are not limited by bob, and bob can send again a day later.
	if _, err := svc.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: "fine"}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	if _, err := svc.SubmitFeedback(asSubject("user_bob"), &corev1.SubmitFeedbackRequest{Body: "next day"}); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackListAndResolve(t *testing.T) {
	svc, now := newFeedbackService(t)
	var ids []string
	for _, sender := range []string{"user_alice", "user_bob", "user_alice"} {
		got, err := svc.SubmitFeedback(asSubject(sender), &corev1.SubmitFeedbackRequest{Body: "from " + sender})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, got.Id)
		*now = now.Add(time.Second)
	}

	// Senders read their own; everyone's is for the team.
	mine, err := svc.ListFeedback(asSubject("user_alice"), &corev1.ListFeedbackRequest{Mine: true})
	if err != nil || len(mine.Feedback) != 2 || mine.Feedback[0].Id != ids[2] || mine.Feedback[1].Id != ids[0] {
		t.Fatalf("alice's own = %+v, %v", mine, err)
	}
	for _, outsider := range []string{"user_alice", "user_dave"} {
		_, err = svc.ListFeedback(asSubject(outsider), &corev1.ListFeedbackRequest{})
		wantFeedbackErr(t, err, codes.PermissionDenied, "owners and admins of gitslice")
	}
	for _, member := range []string{"user_carol", "agent_op"} {
		all, err := svc.ListFeedback(asSubject(member), &corev1.ListFeedbackRequest{})
		if err != nil || len(all.Feedback) != 3 || all.Feedback[0].Id != ids[2] {
			t.Fatalf("%s: all = %+v, %v", member, all, err)
		}
	}

	// Pages follow the cursor, newest first.
	var paged []string
	cursor := ""
	for {
		page, err := svc.ListFeedback(asSubject("user_carol"), &corev1.ListFeedbackRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range page.Feedback {
			paged = append(paged, f.Id)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(paged, ",") != strings.Join([]string{ids[2], ids[1], ids[0]}, ",") {
		t.Fatalf("paged = %v", paged)
	}
	_, err = svc.ListFeedback(asSubject("user_carol"), &corev1.ListFeedbackRequest{Cursor: "nope"})
	wantFeedbackErr(t, err, codes.InvalidArgument, "cursor")

	// The team answers; the sender reads the answer.
	_, err = svc.ResolveFeedback(asSubject("user_alice"), &corev1.ResolveFeedbackRequest{Id: ids[0], Resolution: "done"})
	wantFeedbackErr(t, err, codes.PermissionDenied, "")
	_, err = svc.ResolveFeedback(asSubject("user_carol"), &corev1.ResolveFeedbackRequest{Id: ids[0]})
	wantFeedbackErr(t, err, codes.InvalidArgument, "resolution is required")
	_, err = svc.ResolveFeedback(asSubject("user_carol"), &corev1.ResolveFeedbackRequest{Id: "fb_missing", Resolution: "x"})
	wantFeedbackErr(t, err, codes.NotFound, "")
	resolved, err := svc.ResolveFeedback(asSubject("user_carol"), &corev1.ResolveFeedbackRequest{Id: ids[0], Resolution: "progress lines ship in v0.4.4"})
	if err != nil || resolved.Status != "resolved" || resolved.ResolvedBy != "carol" || resolved.ResolvedAt == "" {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	open, err := svc.ListFeedback(asSubject("user_carol"), &corev1.ListFeedbackRequest{Status: "open"})
	if err != nil || len(open.Feedback) != 2 {
		t.Fatalf("open = %+v, %v", open, err)
	}
	mine, err = svc.ListFeedback(asSubject("user_alice"), &corev1.ListFeedbackRequest{Mine: true, Status: "resolved"})
	if err != nil || len(mine.Feedback) != 1 || mine.Feedback[0].Resolution != "progress lines ship in v0.4.4" {
		t.Fatalf("alice's resolved = %+v, %v", mine, err)
	}
	reopened, err := svc.ResolveFeedback(asSubject("agent_op"), &corev1.ResolveFeedbackRequest{Id: ids[0], Reopen: true})
	if err != nil || reopened.Status != "open" || reopened.Resolution != "progress lines ship in v0.4.4" {
		t.Fatalf("reopened = %+v, %v", reopened, err)
	}
	_, err = svc.ListFeedback(asSubject("user_carol"), &corev1.ListFeedbackRequest{Status: "pending"})
	wantFeedbackErr(t, err, codes.InvalidArgument, "open, resolved or all")
}

func TestFeedbackOperatorsOnlyWithoutAccount(t *testing.T) {
	svc, _ := newFeedbackService(t)
	svc.Account = ""
	_, err := svc.ListFeedback(asSubject("user_carol"), &corev1.ListFeedbackRequest{})
	wantFeedbackErr(t, err, codes.PermissionDenied, "only the server's operators")
	if _, err := svc.ListFeedback(asSubject("agent_op"), &corev1.ListFeedbackRequest{}); err != nil {
		t.Fatal(err)
	}
	var missing *FeedbackService
	_, err = missing.SubmitFeedback(asSubject("user_alice"), &corev1.SubmitFeedbackRequest{Body: "x"})
	wantFeedbackErr(t, err, codes.Unimplemented, "")
}
