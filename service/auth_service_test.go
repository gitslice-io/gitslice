package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestChooseUsernameCreatesHomeSliceInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), storage.ProviderClerk, "clerk_nico", "nico@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	chosen, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "nico"})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.SubjectId != subjectID || chosen.Account != "nico" {
		t.Fatalf("unexpected choose-username response: %#v", chosen)
	}
	slice, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "nico", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(slice.Definition.IncludedPaths, ",") != "/nico" {
		t.Fatalf("home included paths = %#v", slice.Definition.IncludedPaths)
	}
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{
		CommitId: ref.CommitId,
		Path:     "/nico",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("home account root entry = %#v, want directory", resolved.Entry)
	}
}

func TestAuthChooseUsernameForExternalSubjectInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), storage.ProviderClerk, "clerk_user_123", "Taylor.Example@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)

	authStatus, err := handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !authStatus.NeedsUsername || len(authStatus.Accounts) != 0 {
		t.Fatalf("auth status = %#v, want needs username with no accounts", authStatus)
	}

	available, err := handlers.Auth.CheckUsernameAvailable(ctx, &corev1.CheckUsernameAvailableRequest{Username: "Taylor_Name"})
	if err != nil {
		t.Fatal(err)
	}
	if !available.Available || available.Normalized != "taylor-name" || available.Reason != "" {
		t.Fatalf("availability = %#v, want available normalized taylor-name", available)
	}

	chosen, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "Taylor_Name"})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.SubjectId != subjectID || chosen.Account != "taylor-name" {
		t.Fatalf("chosen username = %#v, want subject %q account taylor-name", chosen, subjectID)
	}

	authStatus, err = handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if authStatus.NeedsUsername || len(authStatus.Accounts) == 0 || authStatus.Accounts[0] != "taylor-name" {
		t.Fatalf("auth status after choose = %#v, want taylor-name account", authStatus)
	}

	retried, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "another-name"})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Account != "taylor-name" {
		t.Fatalf("retried choose account = %q, want existing taylor-name", retried.Account)
	}

	available, err = handlers.Auth.CheckUsernameAvailable(ctx, &corev1.CheckUsernameAvailableRequest{Username: "taylor_name"})
	if err != nil {
		t.Fatal(err)
	}
	if available.Available || available.Normalized != "taylor-name" || available.Reason != "username is taken" {
		t.Fatalf("taken availability = %#v, want taken taylor-name", available)
	}
}

func TestAuthUsernamePolicyInMemoryStorage(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	for _, test := range []struct {
		name   string
		input  string
		reason string
	}{
		{name: "minimum length", input: "abc", reason: "username must be at least 4 characters"},
		{name: "reserved brand", input: "OpenAI", reason: "username is reserved"},
		{name: "reserved public figure", input: "Taylor_Swift", reason: "username is reserved"},
	} {
		t.Run(test.name, func(t *testing.T) {
			available, err := handlers.Auth.CheckUsernameAvailable(ctx, &corev1.CheckUsernameAvailableRequest{Username: test.input})
			if err != nil {
				t.Fatal(err)
			}
			if available.Available || available.Reason != test.reason {
				t.Fatalf("availability = %#v, want unavailable reason %q", available, test.reason)
			}

			_, err = handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: test.input})
			if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("ChooseUsername(%q) error = %v, want InvalidArgument containing %q", test.input, err, test.reason)
			}
		})
	}
}

func TestAuthCLILoginDeviceFlowInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := context.Background()

	start, err := handlers.Auth.StartCliLogin(ctx, &corev1.StartCliLoginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if start.Code == "" || start.ExpiresAt == "" || start.PollIntervalSeconds != 2 {
		t.Fatalf("start CLI login = %#v, want code, expiry, poll interval 2", start)
	}

	pending, err := handlers.Auth.PollCliLogin(ctx, &corev1.PollCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.Token != "" || pending.SubjectId != "" {
		t.Fatalf("pending poll = %#v, want pending without token", pending)
	}

	completeCtx := authctx.WithSubjectID(ctx, "user_alice")
	complete, err := handlers.Auth.CompleteCliLogin(completeCtx, &corev1.CompleteCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if complete.SubjectId != "user_alice" {
		t.Fatalf("complete subject = %q, want user_alice", complete.SubjectId)
	}

	approved, err := handlers.Auth.PollCliLogin(ctx, &corev1.PollCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.Token == "" || approved.SubjectId != "user_alice" {
		t.Fatalf("approved poll = %#v, want approved token for user_alice", approved)
	}
	subject, err := mem.Auth.SubjectForToken(ctx, approved.Token)
	if err != nil {
		t.Fatal(err)
	}
	if subject.ID != "user_alice" {
		t.Fatalf("session subject = %q, want user_alice", subject.ID)
	}

	second, err := handlers.Auth.PollCliLogin(ctx, &corev1.PollCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "expired" || second.Token != "" || second.SubjectId != "" {
		t.Fatalf("second poll = %#v, want expired without token", second)
	}
}
