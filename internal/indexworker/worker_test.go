package indexworker

import (
	"context"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/storage"
)

func TestWorkerSetGateBlocksWhileIdleAndWakes(t *testing.T) {
	store := newFakeDerivedIndexStore()
	worker := New(store, 1, 5*time.Millisecond, 20*time.Millisecond)
	gate := activity.NewIdle(time.Minute)
	worker.SetGate(gate)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	time.Sleep(40 * time.Millisecond)
	if calls, _ := store.counts(); calls != 0 {
		t.Fatalf("ProcessOutbox called %d times while custom gate is idle, want 0", calls)
	}

	end := gate.Begin()
	end()
	waitForProcessCall(t, store.calls, time.Second)
}

func TestWorkerEmptyPollsBackOff(t *testing.T) {
	const (
		base   = 5 * time.Millisecond
		window = 230 * time.Millisecond
	)
	store := newFakeDerivedIndexStore()
	worker := New(store, 1, base, 80*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()

	time.Sleep(window)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}

	calls, depthCalls := store.counts()
	fixedIntervalCalls := int(window / base)
	if calls < 2 {
		t.Fatalf("ProcessOutbox called %d times, want at least two polls", calls)
	}
	if calls >= fixedIntervalCalls/3 {
		t.Fatalf("ProcessOutbox called %d times over %s; fixed cadence would be about %d calls", calls, window, fixedIntervalCalls)
	}
	if depthCalls != 1 {
		t.Fatalf("OutboxDepth called %d times during idle polling, want 1", depthCalls)
	}
}

func TestWorkerNudgeWakesAndResetsCadence(t *testing.T) {
	store := newFakeDerivedIndexStore()
	worker := New(store, 1, 10*time.Millisecond, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	for i := 0; i < 3; i++ {
		waitForProcessCall(t, store.calls, 500*time.Millisecond)
	}

	nudgedAt := time.Now()
	worker.Nudge()
	nudgePoll := waitForProcessCall(t, store.calls, 100*time.Millisecond)
	if elapsed := nudgePoll.at.Sub(nudgedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("nudged poll took %s, want no more than 100ms", elapsed)
	}

	followUp := waitForProcessCall(t, store.calls, 100*time.Millisecond)
	if elapsed := followUp.at.Sub(nudgePoll.at); elapsed > 100*time.Millisecond {
		t.Fatalf("post-nudge empty poll took %s, want reset backoff cadence", elapsed)
	}
}

func TestWorkerProcessedWorkRepollsImmediately(t *testing.T) {
	store := newFakeDerivedIndexStore()
	worker := New(store, 1, 10*time.Millisecond, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	waitForProcessCall(t, store.calls, 100*time.Millisecond)
	store.enqueue(storage.OutboxProcessResult{Processed: 1}, nil)

	worker.Nudge()
	processed := waitForProcessedCall(t, store.calls, 100*time.Millisecond)
	followUp := waitForProcessCall(t, store.calls, 100*time.Millisecond)
	if elapsed := followUp.at.Sub(processed.at); elapsed > 100*time.Millisecond {
		t.Fatalf("poll after processed work took %s, want immediate re-poll", elapsed)
	}

	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		_, depthCalls := store.counts()
		if depthCalls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("OutboxDepth called %d times, want a sample after processed work", depthCalls)
		}
		time.Sleep(time.Millisecond)
	}
}

type processResponse struct {
	result storage.OutboxProcessResult
	err    error
}

type processCall struct {
	at     time.Time
	result storage.OutboxProcessResult
}

type fakeDerivedIndexStore struct {
	mu           sync.Mutex
	responses    []processResponse
	processCalls int
	depthCalls   int
	calls        chan processCall
}

func newFakeDerivedIndexStore() *fakeDerivedIndexStore {
	return &fakeDerivedIndexStore{calls: make(chan processCall, 128)}
}

func (s *fakeDerivedIndexStore) ProcessOutbox(context.Context, int) (storage.OutboxProcessResult, error) {
	s.mu.Lock()
	response := processResponse{}
	if len(s.responses) > 0 {
		response = s.responses[0]
		s.responses = s.responses[1:]
	}
	s.processCalls++
	s.mu.Unlock()

	s.calls <- processCall{at: time.Now(), result: response.result}
	return response.result, response.err
}

func (s *fakeDerivedIndexStore) OutboxDepth(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.depthCalls++
	return 0, nil
}

func (s *fakeDerivedIndexStore) WaitForOutboxDrain(context.Context) error {
	return nil
}

func (s *fakeDerivedIndexStore) RebuildDerivedIndexes(context.Context, string) error {
	return nil
}

func (s *fakeDerivedIndexStore) enqueue(result storage.OutboxProcessResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, processResponse{result: result, err: err})
}

func (s *fakeDerivedIndexStore) counts() (processCalls, depthCalls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processCalls, s.depthCalls
}

func waitForProcessCall(t *testing.T, calls <-chan processCall, timeout time.Duration) processCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for ProcessOutbox call", timeout)
		return processCall{}
	}
}

func waitForProcessedCall(t *testing.T, calls <-chan processCall, timeout time.Duration) processCall {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case call := <-calls:
			if call.result.Processed > 0 {
				return call
			}
		case <-timer.C:
			t.Fatalf("timed out after %s waiting for processed ProcessOutbox call", timeout)
			return processCall{}
		}
	}
}
