package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gitslice-io/gitslice/internal/postgres"
)

const defaultPublishInterval = 25 * time.Millisecond

func runPublisher(ctx context.Context, store *postgres.Store, batchSize int, interval time.Duration) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			published, err := store.PublishPending(ctx, batchSize)
			if err != nil && ctx.Err() == nil {
				slog.Warn("pending publish batch failed", "error", err)
			}
			if published > 0 {
				timer.Reset(0)
			} else {
				timer.Reset(interval)
			}
		}
	}
}
