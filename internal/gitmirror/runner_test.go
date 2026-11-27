package gitmirror

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestRunnerFanOutDrainAndBackoff(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	slice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	ctx := context.Background()
	if _, err := mem.Slices.CreateSecret(ctx, "acme", "MIRROR_TOK", "tok_123", []string{"/acme/payment"}, "user_alice"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	mem.GitMirrors.Now = func() time.Time { return now }

	var pushes []Target
	var pushErr error
	r := &Runner{
		Mirrors:             mem.GitMirrors,
		Slices:              mem.Slices,
		AllowPrivateTargets: true,
		Now:                 func() time.Time { return now },
	}
	r.SetPusher(func(_ context.Context, _, _ string, target Target) (*PushResult, error) {
		pushes = append(pushes, target)
		if pushErr != nil {
			return nil, pushErr
		}
		return &PushResult{Head: "abc123", NativeCommitID: "sha256:n1", Exported: 1}, nil
	}, nil)

	if _, err := mem.GitMirrors.PutGitMirror(ctx, storage.GitMirror{
		SliceID: slice.Id, URL: "/tmp/mirror.git", Branch: "main", Username: "bot", TokenSecret: "MIRROR_TOK", CreatedBy: "user_alice",
	}); err != nil {
		t.Fatal(err)
	}

	r.Drain(ctx)
	if len(pushes) != 1 || pushes[0].Token != "tok_123" || pushes[0].URL != "/tmp/mirror.git" {
		t.Fatalf("pushes = %+v", pushes)
	}

	// Non-matching events do not mark pending; matching ones do.
	r.FanOut(ctx, storage.Event{Kind: storage.EventChangesetSubmitted, TargetRef: storage.DefaultTargetRef, ChangedPaths: []string{"/acme/other/a.go"}})
	if m, _ := mem.GitMirrors.GetGitMirror(ctx, slice.Id); m.Pending() {
		t.Fatal("unrelated path marked mirror pending")
	}
	r.FanOut(ctx, storage.Event{Kind: storage.EventChangesetSubmitted, TargetRef: storage.DefaultTargetRef, ChangedPaths: []string{"/acme/payment/a.go"}})
	if m, _ := mem.GitMirrors.GetGitMirror(ctx, slice.Id); !m.Pending() {
		t.Fatal("matching path did not mark mirror pending")
	}

	// Failure backs off until NextAttemptAt.
	pushErr = errors.New("rejected")
	r.Drain(ctx)
	m, _ := mem.GitMirrors.GetGitMirror(ctx, slice.Id)
	if !m.Pending() || m.Failures != 1 || !strings.Contains(m.LastError, "rejected") {
		t.Fatalf("after failure: %+v", m)
	}
	r.Drain(ctx)
	if len(pushes) != 2 {
		t.Fatalf("retried before backoff: %d pushes", len(pushes))
	}
	now = now.Add(2 * time.Minute)
	pushErr = nil
	r.Drain(ctx)
	if len(pushes) != 3 {
		t.Fatalf("not retried after backoff: %d pushes", len(pushes))
	}

	// Tag event marks pending.
	r.FanOut(ctx, storage.Event{Kind: storage.EventTagCreated, SliceID: slice.Id})
	if m, _ := mem.GitMirrors.GetGitMirror(ctx, slice.Id); !m.Pending() {
		t.Fatal("tag event did not mark mirror pending")
	}
}

func TestRunnerValidationAndPrivateHosts(t *testing.T) {
	r := &Runner{AllowPrivateTargets: false}
	for _, raw := range []string{"http://github.com/a/b.git", "https://127.0.0.1/a.git", "/tmp/a.git"} {
		if _, err := r.ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) succeeded with private targets disallowed", raw)
		}
	}
	r.AllowPrivateTargets = true
	if got, err := r.ValidateURL("/tmp/a.git"); err != nil || got != "/tmp/a.git" {
		t.Fatalf("ValidateURL(/tmp/a.git) = (%q, %v)", got, err)
	}

	if got, err := ValidateBranch(""); err != nil || got != "main" {
		t.Fatalf("ValidateBranch(\"\") = (%q, %v)", got, err)
	}
	for _, bad := range []string{"../main", "-bad", "/lead", "trail/", "a.lock"} {
		if _, err := ValidateBranch(bad); err == nil {
			t.Errorf("ValidateBranch(%q) succeeded", bad)
		}
	}
	for _, bad := range []string{"a:b", "a@b", "a/b", "a b"} {
		if _, err := ValidateUsername(bad); err == nil {
			t.Errorf("ValidateUsername(%q) succeeded", bad)
		}
	}
}
