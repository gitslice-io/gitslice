package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"golang.org/x/sync/errgroup"
)

// Warming blames (design/27_blame.md): a file's first blame walks its history,
// seconds for a file with a long one. A landing changes only a few files, so
// the server blames them itself, shortly after each landing, and stores the
// result: the next person to open one finds a snapshot and pays a diff, not a
// walk. Files nobody blamed before are warmed too, as a landing is the moment
// their history is shortest to read.
const (
	blameWarmInterval    = 3 * time.Second
	blameWarmMaxFiles    = 60      // changed files warmed per round, at most
	blameWarmMaxBytes    = 1 << 20 // larger files are left to whoever opens them
	blameWarmConcurrency = 4
	blameWarmRetries     = 4 // a file whose history was not indexed yet is tried again
	blameWarmAncestry    = 100
)

// RunBlameWarmer warms the blames of the files each landing on the default
// ref changed, until ctx ends. It starts from the head as it is now, so a
// restart does not warm the past.
func (s *RepositoryService) RunBlameWarmer(ctx context.Context) {
	if s.Blames == nil {
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
		last = s.warmBlamesOnce(ctx, last, retry)
		timer.Reset(blameWarmInterval)
	}
}

// warmBlamesOnce warms what changed since the head last, plus what is waiting
// to be tried again, and returns the head it warmed for.
func (s *RepositoryService) warmBlamesOnce(ctx context.Context, last string, retry map[string]int) string {
	head, err := s.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		return last
	}
	if last == "" {
		return head.CommitId
	}
	var files []string
	if head.CommitId != last {
		files = s.changedFilesSince(ctx, head.CommitId, last)
	}
	for p := range retry {
		files = append(files, p)
	}
	files = dedupe(files)
	if len(files) > blameWarmMaxFiles {
		files = files[:blameWarmMaxFiles]
	}
	if len(files) == 0 {
		return head.CommitId
	}
	started := time.Now()
	var mu sync.Mutex
	warmed, deferred := 0, 0
	var group errgroup.Group
	group.SetLimit(blameWarmConcurrency)
	for _, p := range files {
		group.Go(func() error {
			outcome := s.warmBlame(ctx, head.CommitId, p)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case outcome == warmTryAgain && retry[p] < blameWarmRetries:
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

// changedFilesSince lists the paths the commits after last, up to head,
// changed (newest first), or just the head's when last is too far back.
func (s *RepositoryService) changedFilesSince(ctx context.Context, head, last string) []string {
	ids, err := s.Repository.CommitAncestry(ctx, head, blameWarmAncestry)
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
	commits, err := s.Repository.GetCommits(ctx, wanted)
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

func dedupe(in []string) []string {
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
	warmSkipped  warmOutcome = iota // not a file to blame, or nothing to store
	warmStored                      // a snapshot was stored
	warmTryAgain                    // history did not reach the file's start yet
)

// warmBlame blames one file at head, which stores its snapshot unless the walk
// could only guess (history not indexed yet), when it asks to be tried again.
func (s *RepositoryService) warmBlame(ctx context.Context, head, p string) warmOutcome {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entry, found, err := s.blameEntry(ctx, head, p)
	if err != nil || !found || entry.Size > blameWarmMaxBytes {
		return warmSkipped
	}
	// The history the blame follows needs no viewer: it is the file's own.
	prefixes := []string{p}
	filters := listCommitFilters{prefixes: prefixes, filtered: true}
	if refs, include, err := s.historyEntityRefsForPathPrefixes(ctx, storage.DefaultTargetRef, p, prefixes); err == nil && len(refs) > 0 {
		filters.entityRefs = refs
		filters.includePrefixesWithEntities = include
	}
	result, err := s.blameWalk(ctx, storage.DefaultTargetRef, filters, head, p, entry)
	switch {
	case err != nil:
		return warmSkipped
	case result.guessed:
		return warmTryAgain
	case result.steps > 0:
		return warmStored
	}
	return warmSkipped
}
