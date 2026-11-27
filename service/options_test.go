package service

import (
	"context"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/doorbell"
	"gitslice.io/gitslice/internal/gitmirror"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestNewWithOptionsAndWorkersInjectActivityGate(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	bell, err := doorbell.New("https://doorbell.example.com", "secret")
	if err != nil {
		t.Fatal(err)
	}
	gate := activity.NewIdle(time.Minute)
	runner := &gitmirror.Runner{
		Mirrors:             mem.GitMirrors,
		Slices:              mem.Slices,
		AllowPrivateTargets: true,
	}
	blobStore := &countingMissingGitBlobStore{BlobStore: mem.Blobs}

	h := New(Stores{
		Auth:        mem.Auth,
		Blobs:       blobStore,
		Changesets:  mem.Changesets,
		Repository:  mem.Repository,
		Slices:      mem.Slices,
		Agents:      mem.Agents,
		Checks:      mem.Checks,
		Feedback:    mem.Feedback,
		Blames:      mem.Blames,
		Automations: mem.Automations,
	}, mem.Objects, nil, Options{
		Doorbell:           bell,
		CIRunners:          []string{"runner_1"},
		FeedbackAccount:    "acme",
		OperatorSubjects:   []string{"user_alice"},
		GitMirrors:         runner,
		AgentSignupEnabled: true,
		ActivityGate:       gate,
	})

	if h.Feedback.Account != "acme" || !h.Auth.AgentSignupEnabled || !h.Agent.dispatcher.onDemand["runner_1"] || h.Agent.doorbell != bell {
		t.Fatalf("NewWithOptions did not wire options cleanly")
	}

	// Verify SliceService uses Options.GitMirrors directly.
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	got, err := h.Slice.SetSliceGitMirror(alice, &corev1.SetSliceGitMirrorRequest{
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Url:   "https://github.com/acme/payment.git",
	})
	if err != nil || got.GetBranch() != "main" {
		t.Fatalf("SetSliceGitMirror with Options.GitMirrors = (%v, %v)", got, err)
	}

	// Verify Workers respects WorkerOptions and injected gate.
	if workers := h.Workers(WorkerOptions{DisableBlameWarmer: true, GitBlobBackfill: false}); len(workers) != 1 {
		t.Fatalf("Workers(sweeper only) len = %d, want 1", len(workers))
	}
	if workers := h.Workers(WorkerOptions{DisableBlameWarmer: false, GitBlobBackfill: true}); len(workers) != 3 {
		t.Fatalf("Workers(all) len = %d, want 3", len(workers))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	backfill := h.NewGitBlobBackfillWorker(nil)
	go func() {
		defer close(done)
		backfill.Run(ctx)
	}()
	time.Sleep(40 * time.Millisecond)
	if calls := blobStore.calls.Load(); calls != 0 {
		cancel()
		<-done
		t.Fatalf("backfill queried %d times while injected gate was idle, want 0", calls)
	}
	gate.Begin()()
	deadline := time.Now().Add(2 * time.Second)
	for blobStore.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if blobStore.calls.Load() == 0 {
		t.Fatal("backfill did not wake after injected gate opened")
	}
}
