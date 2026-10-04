package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/objectid"
)

const (
	gitBlobBackfillBatch       = 200
	gitBlobBackfillConcurrency = 4
	gitBlobBackfillIdle        = 30 * time.Second
)

// BackfillGitBlobIDs computes the Git blob id of blobs uploaded before ids were
// recorded. It makes one pass over at most limit blobs, reading each file once
// and streaming it through the hash, and reports how many it recorded and how
// many it could not read.
func (s *BlobService) BackfillGitBlobIDs(ctx context.Context, limit, concurrency int) (done, failed int, err error) {
	blobs, err := s.Blobs.ListMissingGitBlobIDs(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	var okCount, failCount atomic.Int64
	var mu sync.Mutex
	ids := map[string]string{}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, blob := range blobs {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			rc, err := s.ObjectStore.Get(ctx, blob.StorageLocation, 0, 0)
			if err != nil {
				slog.Warn("git id backfill: cannot read blob", "content_hash", blob.ContentHash, "error", err)
				failCount.Add(1)
				return
			}
			defer rc.Close()
			id, err := objectid.GitBlobIDReader(blob.Size, rc)
			if err != nil {
				slog.Warn("git id backfill: cannot hash blob", "content_hash", blob.ContentHash, "error", err)
				failCount.Add(1)
				return
			}
			mu.Lock()
			ids[blob.ContentHash] = id
			mu.Unlock()
			okCount.Add(1)
		}()
	}
	wg.Wait()
	if len(ids) > 0 {
		if err := s.Blobs.SetGitBlobIDs(ctx, ids); err != nil {
			return 0, int(failCount.Load()), err
		}
	}
	return int(okCount.Load()), int(failCount.Load()), nil
}

// RunGitBlobBackfill works through every blob without a Git id, then checks
// again every half minute for new ones (uploads record their own). Blobs it
// cannot read are retried on the next pass.
func (s *BlobService) RunGitBlobBackfill(ctx context.Context) {
	total := 0
	for ctx.Err() == nil {
		done, failed, err := s.BackfillGitBlobIDs(ctx, gitBlobBackfillBatch, gitBlobBackfillConcurrency)
		total += done
		if err != nil {
			slog.Warn("git id backfill failed", "error", err)
		}
		if done > 0 {
			slog.Info("git id backfill progress", "recorded", done, "unreadable", failed, "total", total)
			continue // more may be waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(gitBlobBackfillIdle):
		}
	}
}
