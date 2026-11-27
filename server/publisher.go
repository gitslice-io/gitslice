package server

import (
	"context"
	"log/slog"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/bgloop"
	"gitslice.io/gitslice/internal/storage"
)

const (
	defaultPublishInterval    = bgloop.DefaultBaseInterval
	defaultPublishMaxInterval = bgloop.DefaultMaxInterval
)

func newPublisherRunner(
	changesets storage.ChangesetStore,
	batchSize int,
	interval, maxInterval time.Duration,
	onPublished func(),
	gate *activity.Gate,
) *bgloop.Runner {
	return bgloop.New(bgloop.Config{
		Name:         "publisher",
		BaseInterval: interval,
		MaxInterval:  maxInterval,
		Gate:         gate,
		Step: func(ctx context.Context) (int, error) {
			published, err := changesets.PublishPending(ctx, batchSize)
			if err != nil && ctx.Err() == nil {
				slog.Warn("pending publish batch failed", "error", err)
			}
			return published, err
		},
		SampleDepth: func(ctx context.Context) {
			depth, depthErr := changesets.PendingPublishDepth(ctx)
			if depthErr == nil {
				storage.SetPendingPublishQueueDepth(depth)
			} else if ctx.Err() == nil {
				slog.Warn("pending publish queue depth sample failed", "error", depthErr)
			}
		},
		OnProcessed: onPublished,
	})
}
