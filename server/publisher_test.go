package server

import (
	"context"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/internal/storage"
)

func TestNextPublishDelay(t *testing.T) {
	const (
		base = 25 * time.Millisecond
		max  = 200 * time.Millisecond
	)

	tests := []struct {
		name        string
		current     time.Duration
		maxInterval time.Duration
		outcome     publishOutcome
		want        time.Duration
	}{
		{name: "empty doubles", current: base, maxInterval: max, outcome: publishEmpty, want: 50 * time.Millisecond},
		{name: "empty doubles again", current: 50 * time.Millisecond, maxInterval: max, outcome: publishEmpty, want: 100 * time.Millisecond},
		{name: "empty clamps to cap", current: 150 * time.Millisecond, maxInterval: max, outcome: publishEmpty, want: max},
		{name: "empty stays at cap", current: max, maxInterval: max, outcome: publishEmpty, want: max},
		{name: "work resets to base", current: max, maxInterval: max, outcome: publishProcessed, want: base},
		{name: "error uses fixed retry", current: base, maxInterval: max, outcome: publishError, want: time.Second},
		{name: "cap below base disables backoff", current: base, maxInterval: time.Millisecond, outcome: publishEmpty, want: base},
		{name: "non-positive cap uses default", current: 40 * time.Second, maxInterval: 0, outcome: publishEmpty, want: defaultPublishMaxInterval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextPublishDelay(tt.current, base, tt.maxInterval, tt.outcome); got != tt.want {
				t.Fatalf("nextPublishDelay(%s, %s, %s, %d) = %s, want %s", tt.current, base, tt.maxInterval, tt.outcome, got, tt.want)
			}
		})
	}
}

func TestDrainPublishRateLimitsDepthSamplesAndSamplesAfterWork(t *testing.T) {
	changesets := &fakePublisherChangesetStore{published: []int{0, 0, 1, 0}}
	state := publisherLoopState{
		delay:        25 * time.Millisecond,
		baseInterval: 25 * time.Millisecond,
		maxInterval:  time.Second,
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	drainPublish(context.Background(), changesets, 1, nil, timer, &state)
	drainPublish(context.Background(), changesets, 1, nil, timer, &state)
	if changesets.depthCalls != 1 {
		t.Fatalf("PendingPublishDepth called %d times during idle polling, want 1", changesets.depthCalls)
	}

	drainPublish(context.Background(), changesets, 1, nil, timer, &state)
	if !state.sampleDepthOnNext {
		t.Fatal("publisher did not request a depth sample after publishing work")
	}
	drainPublish(context.Background(), changesets, 1, nil, timer, &state)
	if changesets.depthCalls != 2 {
		t.Fatalf("PendingPublishDepth called %d times, want a second sample after published work", changesets.depthCalls)
	}
}

type fakePublisherChangesetStore struct {
	storage.ChangesetStore
	published  []int
	depthCalls int
}

func (s *fakePublisherChangesetStore) PublishPending(context.Context, int) (int, error) {
	published := s.published[0]
	s.published = s.published[1:]
	return published, nil
}

func (s *fakePublisherChangesetStore) PendingPublishDepth(context.Context) (int, error) {
	s.depthCalls++
	return 0, nil
}
