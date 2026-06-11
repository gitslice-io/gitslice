package indexworker

import (
	"context"
	"log/slog"
	"time"

	"github.com/gitslice-io/gitslice/internal/storage"
)

type Worker struct {
	store     storage.DerivedIndexStore
	batchSize int
	interval  time.Duration
	nudges    chan struct{}
}

func New(store storage.DerivedIndexStore, batchSize int, interval time.Duration) *Worker {
	if batchSize <= 0 {
		batchSize = 128
	}
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	return &Worker{
		store:     store,
		batchSize: batchSize,
		interval:  interval,
		nudges:    make(chan struct{}, 1),
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
			w.runOnce(ctx, timer)
		case <-timer.C:
			w.runOnce(ctx, timer)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context, timer *time.Timer) {
	depth, depthErr := w.store.OutboxDepth(ctx)
	if depthErr == nil {
		storage.SetOutboxQueueDepth(depth)
	} else if ctx.Err() == nil {
		slog.Warn("outbox queue depth sample failed", "error", depthErr)
	}
	result, err := w.store.ProcessOutbox(ctx, w.batchSize)
	if err != nil && ctx.Err() == nil {
		slog.Warn("outbox batch failed", "error", err)
	}
	if err == nil {
		storage.RecordOutboxProcessResult(result)
	}
	if err == nil && result.Failed == 0 && result.Processed > 0 {
		resetTimer(timer, 0)
		return
	}
	resetTimer(timer, w.interval)
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
