package indexworker

import (
	"context"
	"log/slog"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/bgloop"
	"gitslice.io/gitslice/internal/storage"
)

const (
	defaultBatchSize    = 128
	defaultInterval     = bgloop.DefaultBaseInterval
	defaultMaxInterval  = bgloop.DefaultMaxInterval
	errorRetryInterval  = bgloop.DefaultErrorRetryInterval
	depthSampleInterval = bgloop.DefaultDepthSampleInterval
)

type Worker struct {
	store     storage.DerivedIndexStore
	batchSize int
	loop      *bgloop.Runner
}

func New(store storage.DerivedIndexStore, batchSize int, interval, maxInterval time.Duration) *Worker {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	w := &Worker{
		store:     store,
		batchSize: batchSize,
	}
	w.loop = bgloop.New(bgloop.Config{
		Name:                "indexworker",
		BaseInterval:        interval,
		MaxInterval:         maxInterval,
		ErrorRetryInterval:  errorRetryInterval,
		DepthSampleInterval: depthSampleInterval,
		SampleDepth:         w.sampleDepth,
		Step:                w.step,
	})
	return w
}

// SetGate injects the activity gate used to pause background polling when the
// server is idle. Passing nil reverts to activity.Default.
func (w *Worker) SetGate(gate *activity.Gate) {
	w.loop.SetGate(gate)
}

func (w *Worker) Nudge() {
	w.loop.Nudge()
}

func (w *Worker) Run(ctx context.Context) {
	w.loop.Run(ctx)
}

func (w *Worker) sampleDepth(ctx context.Context) {
	depth, depthErr := w.store.OutboxDepth(ctx)
	if depthErr == nil {
		storage.SetOutboxQueueDepth(depth)
	} else if ctx.Err() == nil {
		slog.Warn("outbox queue depth sample failed", "error", depthErr)
	}
}

func (w *Worker) step(ctx context.Context) (int, error) {
	result, err := w.store.ProcessOutbox(ctx, w.batchSize)
	if err != nil && ctx.Err() == nil {
		slog.Warn("outbox batch failed", "error", err)
	}
	if err == nil {
		storage.RecordOutboxProcessResult(result)
	}
	return result.Processed, err
}
