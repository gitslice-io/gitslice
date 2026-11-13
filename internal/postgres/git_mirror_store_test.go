package postgres

import (
	"errors"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

var _ storage.GitMirrorStore = (*GitMirrorStore)(nil)

func TestGitMirrorStore(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slice, err := store.Slices().Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "mirrored"}, []string{"/acme/mirrored"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := store.GitMirrors()
	if _, err := mirrors.GetGitMirror(ctx, slice.Id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing mirror: %v", err)
	}
	m, err := mirrors.PutGitMirror(ctx, storage.GitMirror{SliceID: slice.Id, URL: "https://github.com/acme/payment.git", TokenSecret: "TOKEN", CreatedBy: "user_alice"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Account != "acme" || m.Slice != "mirrored" || m.Branch != "main" || !m.Pending() {
		t.Fatalf("put = %+v", m)
	}

	claimed, err := mirrors.ClaimDueGitMirrors(ctx, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if again, _ := mirrors.ClaimDueGitMirrors(ctx, 10, time.Minute); len(again) != 0 {
		t.Fatal("a leased mirror was claimed twice")
	}
	seq := claimed[0].PendingSeq
	// Something lands during the push: it stays pending afterwards.
	if err := mirrors.MarkGitMirrorsPending(ctx, []string{slice.Id}, false); err != nil {
		t.Fatal(err)
	}
	if err := mirrors.FinishGitMirror(ctx, slice.Id, storage.GitMirrorPush{Seq: seq, OK: true, Commit: "abc", Native: "sha256:n"}); err != nil {
		t.Fatal(err)
	}
	m, _ = mirrors.GetGitMirror(ctx, slice.Id)
	if !m.Pending() || m.LastPushedCommit != "abc" || m.LastPushedNative != "sha256:n" || m.LastPushedAt.IsZero() || !m.LeaseUntil.IsZero() {
		t.Fatalf("after a push with more pending: %+v", m)
	}

	// A failure backs off.
	claimed, _ = mirrors.ClaimDueGitMirrors(ctx, 10, time.Minute)
	if len(claimed) != 1 {
		t.Fatal("pending mirror not claimed")
	}
	if err := mirrors.FinishGitMirror(ctx, slice.Id, storage.GitMirrorPush{Seq: claimed[0].PendingSeq, Error: "rejected", NextAttemptAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if again, _ := mirrors.ClaimDueGitMirrors(ctx, 10, time.Minute); len(again) != 0 {
		t.Fatal("claimed during backoff")
	}
	m, _ = mirrors.GetGitMirror(ctx, slice.Id)
	if m.Failures != 1 || m.LastError != "rejected" || m.LastPushedCommit != "abc" {
		t.Fatalf("after a failure: %+v", m)
	}
	// "Push now" clears the backoff.
	if err := mirrors.MarkGitMirrorsPending(ctx, []string{slice.Id}, true); err != nil {
		t.Fatal(err)
	}
	if again, _ := mirrors.ClaimDueGitMirrors(ctx, 10, time.Minute); len(again) != 1 {
		t.Fatal("push now did not make it due")
	}
	list, err := mirrors.ListGitMirrors(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := mirrors.DeleteGitMirror(ctx, slice.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := mirrors.GetGitMirror(ctx, slice.Id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
