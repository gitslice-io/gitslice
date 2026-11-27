package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/storage"
)

func TestPublisherRunnerNudgePublishesSamplesDepthAndHonorsGate(t *testing.T) {
	gate := activity.NewIdle(time.Minute)
	firstDone := make(chan struct{})
	allDone := make(chan struct{})
	store := &fakePublisherChangesetStore{
		published: []int{2, 0},
		firstDone: firstDone,
		allDone:   allDone,
	}
	var notified atomic.Int32
	runner := newPublisherRunner(store, 10, 5*time.Millisecond, 50*time.Millisecond, func() {
		notified.Add(1)
	}, gate)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()

	// Idle gate blocks timer-driven polls, but Nudge wakes immediately for pass 1.
	runner.Nudge()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for nudged publish pass")
	}

	// Waking the gate allows the timer-driven follow-up pass to run and re-sample depth.
	endReq := gate.Begin()
	endReq()
	select {
	case <-allDone:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for gate-woken follow-up publish pass")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher runner did not exit after cancel")
	}

	if got := notified.Load(); got != 1 {
		t.Fatalf("onPublished calls = %d, want 1", got)
	}
	store.mu.Lock()
	depthCalls := store.depthCalls
	store.mu.Unlock()
	if depthCalls < 2 {
		t.Fatalf("PendingPublishDepth calls = %d, want >= 2", depthCalls)
	}
}

type fakePublisherChangesetStore struct {
	storage.ChangesetStore
	mu         sync.Mutex
	published  []int
	depthCalls int
	firstOnce  sync.Once
	allOnce    sync.Once
	firstDone  chan struct{}
	allDone    chan struct{}
}

func (s *fakePublisherChangesetStore) PublishPending(context.Context, int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.published) == 0 {
		s.allOnce.Do(func() { close(s.allDone) })
		return 0, nil
	}
	published := s.published[0]
	s.published = s.published[1:]
	s.firstOnce.Do(func() { close(s.firstDone) })
	if len(s.published) == 0 {
		s.allOnce.Do(func() { close(s.allDone) })
	}
	return published, nil
}

func (s *fakePublisherChangesetStore) PendingPublishDepth(context.Context) (int, error) {
	s.mu.Lock()
	s.depthCalls++
	s.mu.Unlock()
	return 0, nil
}
