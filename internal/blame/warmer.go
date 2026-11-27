package blame

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/storage"
	"golang.org/x/sync/errgroup"
)

const (
	WarmInterval              = 3 * time.Second
	WarmMaxFiles              = 60      // changed files warmed per round, at most
	WarmMaxBytes              = 1 << 20 // larger files are left to whoever opens them
	WarmConcurrency           = 4
	WarmRetries               = 4 // a file whose history was not indexed yet is tried again
	WarmAncestry              = 100
	WarmBulkImportQuietWindow = 15 * time.Second
)

// Warmer pre-computes blame snapshots for files touched by recent landings on
// the default ref (design/27_blame.md).
type Warmer struct {
	Engine *Engine
	// Gate controls idle sleep; defaults to activity.Default when nil.
	Gate *activity.Gate
	// ShouldPause optionally returns true while a bulk import is active so
	// warming pauses until it settles.
	ShouldPause func() bool
	// Interval overrides WarmInterval when positive.
	Interval time.Duration
}

// NewWarmer creates a standalone blame warmer worker.
func NewWarmer(engine *Engine, gate *activity.Gate, shouldPause func() bool) *Warmer {
	return &Warmer{
		Engine:      engine,
		Gate:        gate,
		ShouldPause: shouldPause,
		Interval:    WarmInterval,
	}
}

func (w *Warmer) gate() *activity.Gate {
	if w != nil && w.Gate != nil {
		return w.Gate
	}
	return activity.Default
}

func (w *Warmer) interval() time.Duration {
	if w != nil && w.Interval > 0 {
		return w.Interval
	}
	return WarmInterval
}

// Run warms the blames of the files each landing on the default ref changed,
// until ctx ends. It starts from the head as it is now, so a restart does not
// warm the past.
func (w *Warmer) Run(ctx context.Context) {
	if w == nil || w.Engine == nil || w.Engine.Blames == nil {
		return
	}
	last := ""
	retry := map[string]int{} // path -> tries so far
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		// An idle server leaves the database alone (internal/activity);
		// landings come with requests, which wake it.
		if w.gate().Wait(ctx) != nil {
			return
		}
		last = w.WarmOnce(ctx, last, retry)
		timer.Reset(w.interval())
	}
}

// WarmOnce warms what changed since last, plus what is waiting to be tried
// again, and returns the head it warmed for.
func (w *Warmer) WarmOnce(ctx context.Context, last string, retry map[string]int) string {
	head, err := w.Engine.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		return last
	}
	if last == "" {
		return head.CommitId
	}
	if w.ShouldPause != nil && w.ShouldPause() {
		clear(retry)
		return head.CommitId
	}
	var files []string
	if head.CommitId != last {
		files = w.changedFilesSince(ctx, head.CommitId, last)
	}
	for p := range retry {
		files = append(files, p)
	}
	files = dedupePaths(files)
	if len(files) > WarmMaxFiles {
		files = files[:WarmMaxFiles]
	}
	if len(files) == 0 {
		return head.CommitId
	}
	started := time.Now()
	var mu sync.Mutex
	warmed, deferred := 0, 0
	var group errgroup.Group
	group.SetLimit(WarmConcurrency)
	for _, p := range files {
		group.Go(func() error {
			outcome := w.warmFile(ctx, head.CommitId, p)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case outcome == warmTryAgain && retry[p] < WarmRetries:
				retry[p]++
				deferred++
			default:
				delete(retry, p)
				if outcome == warmStored {
					warmed++
				}
			}
			return nil
		})
	}
	_ = group.Wait()
	slog.Info("blame warm", "head", head.CommitId, "files", len(files), "stored", warmed, "again_later", deferred, "ms", time.Since(started).Milliseconds())
	return head.CommitId
}

func (w *Warmer) changedFilesSince(ctx context.Context, head, last string) []string {
	ids, err := w.Engine.Repository.CommitAncestry(ctx, head, WarmAncestry)
	if err != nil {
		return nil
	}
	var wanted []string
	for _, id := range ids {
		if id == last {
			break
		}
		wanted = append(wanted, id)
	}
	if len(wanted) == len(ids) && len(ids) > 0 {
		wanted = ids[:1]
	}
	commits, err := w.Engine.Repository.GetCommits(ctx, wanted)
	if err != nil {
		return nil
	}
	var files []string
	for _, id := range wanted {
		if commit := commits[id]; commit != nil {
			files = append(files, commit.ChangedPaths...)
		}
	}
	return files
}

func dedupePaths(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, p := range in {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

type warmOutcome int

const (
	warmSkipped warmOutcome = iota
	warmStored
	warmTryAgain
)

func (w *Warmer) warmFile(ctx context.Context, head, p string) warmOutcome {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entry, found, err := w.Engine.Entry(ctx, head, p)
	if err != nil || !found || entry.Size > WarmMaxBytes {
		return warmSkipped
	}
	prefixes := []string{p}
	filters := CommitFilters{Prefixes: prefixes, Filtered: true}
	if refs, include, err := w.Engine.HistoryEntityRefsForPathPrefixes(ctx, storage.DefaultTargetRef, p, prefixes); err == nil && len(refs) > 0 {
		filters.EntityRefs = refs
		filters.IncludePrefixesWithEntities = include
	}
	result, err := w.Engine.Walk(ctx, storage.DefaultTargetRef, filters, head, p, entry, nil)
	switch {
	case err != nil:
		return warmSkipped
	case result.Guessed:
		return warmTryAgain
	case result.Steps > 0:
		return warmStored
	}
	return warmSkipped
}
