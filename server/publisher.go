package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gitslice-io/gitslice/internal/storage"
)

const defaultPublishInterval = 25 * time.Millisecond

func runPublisher(ctx context.Context, changesets storage.ChangesetStore, batchSize int, interval time.Duration, onPublished func()) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			depth, depthErr := changesets.PendingPublishDepth(ctx)
			if depthErr == nil {
				storage.SetPendingPublishQueueDepth(depth)
			} else if ctx.Err() == nil {
				slog.Warn("pending publish queue depth sample failed", "error", depthErr)
			}
			published, err := changesets.PublishPending(ctx, batchSize)
			if err != nil && ctx.Err() == nil {
				slog.Warn("pending publish batch failed", "error", err)
			}
			if published > 0 {
				if onPublished != nil {
					onPublished()
				}
				timer.Reset(0)
			} else {
				timer.Reset(interval)
			}
		}
	}
}
