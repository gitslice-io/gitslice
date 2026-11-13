package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"gitslice.io/gitslice/internal/gitcompat"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type fakeMirrorPush struct {
	targets []gitcompat.MirrorTarget
	err     error
}

func (f *fakeMirrorPush) push(ctx context.Context, account, slice string, target gitcompat.MirrorTarget) (*gitcompat.MirrorResult, error) {
	f.targets = append(f.targets, target)
	if f.err != nil {
		return nil, f.err
	}
	return &gitcompat.MirrorResult{Head: "abc123", NativeCommitID: "sha256:n1", Exported: 1}, nil
}

func newGitMirrorHandlers(t *testing.T) (*Handlers, *GitMirrors, *fakeMirrorPush, *time.Time) {
	t.Helper()
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_bob", "acme", "writer")
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	mem.GitMirrors.Now = func() time.Time { return now }
	fake := &fakeMirrorPush{}
	mirrors := &GitMirrors{Mirrors: mem.GitMirrors, Slices: mem.Slices, AllowPrivateTargets: true, Now: func() time.Time { return now }}
	mirrors.SetPusher(fake.push, nil)
	h.Slice.GitMirrors = mirrors
	if _, err := h.Secret.CreateSecret(subjectCtx("user_alice"), &corev1.CreateSecretRequest{
		Account: "acme", Name: "GITHUB_MIRROR_TOKEN", Value: "ghp_token",
		Scope: &corev1.SecretScope{Paths: []string{"/acme/payment"}},
	}); err != nil {
		t.Fatal(err)
	}
	return h, mirrors, fake, &now
}

func TestGitMirrorSettingIsForOwners(t *testing.T) {
	h, _, _, _ := newGitMirrorHandlers(t)
	alice, bob := subjectCtx("user_alice"), subjectCtx("user_bob")

	_, err := h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef})
	wantCode(t, err, codes.NotFound, "no mirror yet")
	req := &corev1.SetSliceGitMirrorRequest{Slice: paymentRef, Url: "https://github.com/acme/payment.git", TokenSecret: "GITHUB_MIRROR_TOKEN"}
	_, err = h.Slice.SetSliceGitMirror(bob, req)
	wantCode(t, err, codes.PermissionDenied, "a writer setting a mirror")

	got, err := h.Slice.SetSliceGitMirror(alice, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetBranch() != "main" || got.GetRoot() != "/acme/payment" || !got.GetPending() || got.GetTokenSecret() != "GITHUB_MIRROR_TOKEN" {
		t.Fatalf("SetSliceGitMirror = %v", got)
	}
	_, err = h.Slice.GetSliceGitMirror(bob, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef})
	wantCode(t, err, codes.PermissionDenied, "a writer reading the mirror")

	for label, bad := range map[string]*corev1.SetSliceGitMirrorRequest{
		"an ssh URL":           {Slice: paymentRef, Url: "git@github.com:acme/payment.git"},
		"credentials in a URL": {Slice: paymentRef, Url: "https://user:pw@github.com/acme/payment.git"},
		"a bad branch":         {Slice: paymentRef, Url: "https://github.com/a/b.git", Branch: "../main"},
		"a bad user name":      {Slice: paymentRef, Url: "https://github.com/a/b.git", Username: "a:b"},
		"a bad secret name":    {Slice: paymentRef, Url: "https://github.com/a/b.git", TokenSecret: "lower"},
	} {
		_, err = h.Slice.SetSliceGitMirror(alice, bad)
		wantCode(t, err, codes.InvalidArgument, label)
	}
	_, err = h.Slice.SetSliceGitMirror(alice, &corev1.SetSliceGitMirrorRequest{Slice: paymentRef, Url: "https://github.com/a/b.git", TokenSecret: "MISSING"})
	wantCode(t, err, codes.FailedPrecondition, "a secret the slice cannot see")

	if _, err = h.Slice.DeleteSliceGitMirror(alice, &corev1.DeleteSliceGitMirrorRequest{Slice: paymentRef}); err != nil {
		t.Fatal(err)
	}
	_, err = h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef})
	wantCode(t, err, codes.NotFound, "after removal")
}

func TestGitMirrorPushesQueuedWorkAndBacksOff(t *testing.T) {
	h, mirrors, fake, now := newGitMirrorHandlers(t)
	ctx := context.Background()
	alice := subjectCtx("user_alice")
	// A local path: private targets are allowed here (tests).
	if _, err := h.Slice.SetSliceGitMirror(alice, &corev1.SetSliceGitMirrorRequest{Slice: paymentRef, Url: "/tmp/mirror.git", TokenSecret: "GITHUB_MIRROR_TOKEN", Username: "bot"}); err != nil {
		t.Fatal(err)
	}
	mirrors.Drain(ctx)
	if len(fake.targets) != 1 || fake.targets[0].Token != "ghp_token" || fake.targets[0].Username != "bot" || fake.targets[0].URL != "/tmp/mirror.git" {
		t.Fatalf("pushes = %+v", fake.targets)
	}
	got, _ := h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef})
	if got.GetPending() || got.GetLastPushedCommit() != "abc123" || got.GetLastPushedNativeCommit() != "sha256:n1" || got.GetLastError() != "" {
		t.Fatalf("after a good push: %v", got)
	}
	// Nothing pending: no push.
	mirrors.Drain(ctx)
	if len(fake.targets) != 1 {
		t.Fatalf("pushed %d times with nothing pending", len(fake.targets))
	}

	// A landing elsewhere does not mark it; one in the slice does.
	mirrors.FanOut(ctx, storage.WebhookEvent{Kind: storage.WebhookEventChangesetSubmitted, TargetRef: storage.DefaultTargetRef, ChangedPaths: []string{"/acme/docs/a.md"}})
	mirrors.FanOut(ctx, storage.WebhookEvent{Kind: storage.WebhookEventChangesetSubmitted, TargetRef: "refs/global/other", ChangedPaths: []string{"/acme/payment/a.go"}})
	if got, _ = h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef}); got.GetPending() {
		t.Fatal("a landing outside the slice (or on another ref) marked the mirror")
	}
	mirrors.FanOut(ctx, storage.WebhookEvent{Kind: storage.WebhookEventChangesetSubmitted, TargetRef: storage.DefaultTargetRef, ChangedPaths: []string{"/acme/payment/a.go"}})
	if got, _ = h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef}); !got.GetPending() {
		t.Fatal("a landing in the slice did not mark the mirror")
	}

	// A failure is recorded, redacted by the pusher, and retried after the
	// backoff only.
	fake.err = errors.New("push mirror branch: rejected")
	mirrors.Drain(ctx)
	got, _ = h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef})
	if !got.GetPending() || got.GetFailures() != 1 || !strings.Contains(got.GetLastError(), "rejected") || got.GetNextAttemptAt() == "" {
		t.Fatalf("after a failure: %v", got)
	}
	mirrors.Drain(ctx)
	if len(fake.targets) != 2 {
		t.Fatalf("retried before the backoff: %d pushes", len(fake.targets))
	}
	*now = now.Add(2 * time.Minute)
	fake.err = nil
	mirrors.Drain(ctx)
	if len(fake.targets) != 3 {
		t.Fatalf("not retried after the backoff: %d pushes", len(fake.targets))
	}

	// A tag of the slice marks it; "push now" skips the wait.
	mirrors.FanOut(ctx, storage.WebhookEvent{Kind: storage.WebhookEventTagCreated, SliceID: mustSliceID(t, h, paymentRef)})
	if got, _ = h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef}); !got.GetPending() {
		t.Fatal("a tag did not mark the mirror")
	}
	if _, err := h.Slice.PushSliceGitMirror(alice, &corev1.PushSliceGitMirrorRequest{Slice: paymentRef}); err != nil {
		t.Fatal(err)
	}
	mirrors.Drain(ctx)
	if len(fake.targets) != 4 {
		t.Fatalf("push now: %d pushes", len(fake.targets))
	}
}

func TestGitMirrorRefusesPrivateHostsWhenNotAllowed(t *testing.T) {
	h, mirrors, fake, _ := newGitMirrorHandlers(t)
	alice := subjectCtx("user_alice")
	mirrors.AllowPrivateTargets = false
	for _, url := range []string{"http://github.com/a/b.git", "https://127.0.0.1/a.git", "https://169.254.169.254/a.git", "/tmp/a.git"} {
		_, err := h.Slice.SetSliceGitMirror(alice, &corev1.SetSliceGitMirrorRequest{Slice: paymentRef, Url: url})
		wantCode(t, err, codes.InvalidArgument, url)
	}
	// A host whose name resolves to a private address fails at push time.
	mirrors.AllowPrivateTargets = true
	if _, err := h.Slice.SetSliceGitMirror(alice, &corev1.SetSliceGitMirrorRequest{Slice: paymentRef, Url: "https://localhost/a.git"}); err != nil {
		t.Fatal(err)
	}
	mirrors.AllowPrivateTargets = false
	mirrors.Drain(context.Background())
	got, _ := h.Slice.GetSliceGitMirror(alice, &corev1.GetSliceGitMirrorRequest{Slice: paymentRef})
	if len(fake.targets) != 0 || got.GetLastError() == "" {
		t.Fatalf("pushed to a private host: %d pushes, error %q", len(fake.targets), got.GetLastError())
	}
}

func mustSliceID(t *testing.T, h *Handlers, ref *corev1.SliceRef) string {
	t.Helper()
	slice, err := h.Slice.Slices.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return slice.GetId()
}
