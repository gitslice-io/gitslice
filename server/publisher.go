package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gitslice-io/gitslice/internal/storage"
)

const defaultPublishInterval = 25 * time.Millisecond

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

func runPublisher(ctx context.Context, changesets storage.ChangesetStore, batchSize int, interval time.Duration, onPublished func(), nudge <-chan struct{}) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-nudge:
			drainPublish(ctx, changesets, batchSize, interval, onPublished, timer)
		case <-timer.C:
			drainPublish(ctx, changesets, batchSize, interval, onPublished, timer)
		}
	}
}

func drainPublish(ctx context.Context, changesets storage.ChangesetStore, batchSize int, interval time.Duration, onPublished func(), timer *time.Timer) {
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
		resetPublishTimer(timer, 0)
		return
	}
	resetPublishTimer(timer, interval)
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
