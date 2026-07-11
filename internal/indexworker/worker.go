package indexworker

import (
	"context"
	"log/slog"
	"time"

	"github.com/gitslice-io/gitslice/internal/storage"
)

const (
	defaultInterval     = 25 * time.Millisecond
	defaultMaxInterval  = 60 * time.Second
	errorRetryInterval  = time.Second
	depthSampleInterval = 30 * time.Second
)

type pollOutcome uint8

const (
	pollEmpty pollOutcome = iota
	pollProcessed
	pollError
)

type Worker struct {
	store             storage.DerivedIndexStore
	batchSize         int
	interval          time.Duration
	maxInterval       time.Duration
	delay             time.Duration
	lastDepthSample   time.Time
	sampleDepthOnNext bool
	nudges            chan struct{}
}

func New(store storage.DerivedIndexStore, batchSize int, interval, maxInterval time.Duration) *Worker {
	if batchSize <= 0 {
		batchSize = 128
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	maxInterval = normalizeMaxInterval(interval, maxInterval)
	return &Worker{
		store:       store,
		batchSize:   batchSize,
		interval:    interval,
		maxInterval: maxInterval,
		delay:       interval,
		nudges:      make(chan struct{}, 1),
	}
}

func (w *Worker) Nudge() {
	select {
	case w.nudges <- struct{}{}:
	default:
	}
}

func (w *Worker) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.nudges:
			w.delay = w.interval
			w.runOnce(ctx, timer)
		case <-timer.C:
			w.runOnce(ctx, timer)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context, timer *time.Timer) {
	now := time.Now()
	if w.sampleDepthOnNext || w.lastDepthSample.IsZero() || now.Sub(w.lastDepthSample) >= depthSampleInterval {
		w.lastDepthSample = now
		w.sampleDepthOnNext = false
		depth, depthErr := w.store.OutboxDepth(ctx)
		if depthErr == nil {
			storage.SetOutboxQueueDepth(depth)
		} else if ctx.Err() == nil {
			slog.Warn("outbox queue depth sample failed", "error", depthErr)
		}
	}
	result, err := w.store.ProcessOutbox(ctx, w.batchSize)
	if err != nil && ctx.Err() == nil {
		slog.Warn("outbox batch failed", "error", err)
	}
	if err == nil {
		storage.RecordOutboxProcessResult(result)
	}
	if result.Processed > 0 {
		w.sampleDepthOnNext = true
	}

	outcome := pollEmpty
	if err != nil {
		outcome = pollError
	} else if result.Processed > 0 {
		outcome = pollProcessed
	}
	w.delay = nextDelay(w.delay, w.interval, w.maxInterval, outcome)
	if outcome == pollProcessed {
		resetTimer(timer, 0)
		return
	}
	resetTimer(timer, w.delay)
}

func nextDelay(current, base, maxInterval time.Duration, outcome pollOutcome) time.Duration {
	switch outcome {
	case pollProcessed:
		return base
	case pollError:
		return errorRetryInterval
	}

	maxInterval = normalizeMaxInterval(base, maxInterval)
	if current < base {
		current = base
	}
	if current >= maxInterval || current > maxInterval-current {
		return maxInterval
	}
	return current * 2
}

func normalizeMaxInterval(base, maxInterval time.Duration) time.Duration {
	if maxInterval <= 0 {
		maxInterval = defaultMaxInterval
	}
	if maxInterval < base {
		return base
	}
	return maxInterval
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
