package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gitslice-io/gitslice/internal/storage"
)

const (
	defaultPublishInterval    = 25 * time.Millisecond
	defaultPublishMaxInterval = 60 * time.Second
	errorRetryInterval        = time.Second
	depthSampleInterval       = 30 * time.Second
)

type publishOutcome uint8

const (
	publishEmpty publishOutcome = iota
	publishProcessed
	publishError
)

type publisherLoopState struct {
	delay             time.Duration
	baseInterval      time.Duration
	maxInterval       time.Duration
	lastDepthSample   time.Time
	sampleDepthOnNext bool
}

type publishNudger struct {
	ch chan struct{}
}

func newPublishNudger() *publishNudger {
	return &publishNudger{ch: make(chan struct{}, 1)}
}

func (n *publishNudger) Nudge() {
	select {
	case n.ch <- struct{}{}:
	default:
	}
}

func runPublisher(ctx context.Context, changesets storage.ChangesetStore, batchSize int, interval time.Duration, onPublished func(), nudge <-chan struct{}, maxInterval time.Duration) {
	if interval <= 0 {
		interval = defaultPublishInterval
	}
	state := publisherLoopState{
		delay:        interval,
		baseInterval: interval,
		maxInterval:  normalizePublishMaxInterval(interval, maxInterval),
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-nudge:
			state.delay = state.baseInterval
			drainPublish(ctx, changesets, batchSize, onPublished, timer, &state)
		case <-timer.C:
			drainPublish(ctx, changesets, batchSize, onPublished, timer, &state)
		}
	}
}

func drainPublish(ctx context.Context, changesets storage.ChangesetStore, batchSize int, onPublished func(), timer *time.Timer, state *publisherLoopState) {
	now := time.Now()
	if state.sampleDepthOnNext || state.lastDepthSample.IsZero() || now.Sub(state.lastDepthSample) >= depthSampleInterval {
		state.lastDepthSample = now
		state.sampleDepthOnNext = false
		depth, depthErr := changesets.PendingPublishDepth(ctx)
		if depthErr == nil {
			storage.SetPendingPublishQueueDepth(depth)
		} else if ctx.Err() == nil {
			slog.Warn("pending publish queue depth sample failed", "error", depthErr)
		}
	}
	published, err := changesets.PublishPending(ctx, batchSize)
	if err != nil && ctx.Err() == nil {
		slog.Warn("pending publish batch failed", "error", err)
	}
	if published > 0 {
		state.sampleDepthOnNext = true
		if onPublished != nil {
			onPublished()
		}
	}

	outcome := publishEmpty
	if err != nil {
		outcome = publishError
	} else if published > 0 {
		outcome = publishProcessed
	}
	state.delay = nextPublishDelay(state.delay, state.baseInterval, state.maxInterval, outcome)
	if outcome == publishProcessed {
		resetPublishTimer(timer, 0)
		return
	}
	resetPublishTimer(timer, state.delay)
}

func nextPublishDelay(current, base, maxInterval time.Duration, outcome publishOutcome) time.Duration {
	switch outcome {
	case publishProcessed:
		return base
	case publishError:
		return errorRetryInterval
	}

	maxInterval = normalizePublishMaxInterval(base, maxInterval)
	if current < base {
		current = base
	}
	if current >= maxInterval || current > maxInterval-current {
		return maxInterval
	}
	return current * 2
}

func normalizePublishMaxInterval(base, maxInterval time.Duration) time.Duration {
	if maxInterval <= 0 {
		maxInterval = defaultPublishMaxInterval
	}
	if maxInterval < base {
		return base
	}
	return maxInterval
}

func resetPublishTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
