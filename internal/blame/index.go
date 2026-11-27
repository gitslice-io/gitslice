package blame

import (
	"context"
	"sync"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// changes is a page's recorded file changes, by commit and path.
type changes map[string]map[string][]storage.FileChange

func newChanges(list []storage.FileChange) changes {
	out := changes{}
	for _, change := range list {
		if out[change.CommitID] == nil {
			out[change.CommitID] = map[string][]storage.FileChange{}
		}
		out[change.CommitID][change.Path] = append(out[change.CommitID][change.Path], change)
	}
	return out
}

// indexStep is one planned step from the history index: created, or parentHash
// at parentPath. redacted is true when a move came from an unreadable path.
type indexStep struct {
	created    bool
	redacted   bool
	parentPath string
	parentHash string
}

func (c changes) one(commitID, path string) (storage.FileChange, bool) {
	rows := c[commitID][path]
	if len(rows) != 1 {
		return storage.FileChange{}, false
	}
	return rows[0], true
}

func (c changes) step(commits []*corev1.Commit, index int, path, hash string, canReadPath func(string) bool) (indexStep, bool) {
	change, ok := c.one(commits[index].Id, path)
	if !ok || change.ContentHash != hash {
		return indexStep{}, false
	}
	prevPath := path
	switch change.ChangeKind {
	case "added":
		if change.OldPath != "" {
			return indexStep{}, false
		}
		return indexStep{created: true}, true
	case "modified":
		if change.OldPath != "" {
			return indexStep{}, false
		}
	case "moved":
		if change.OldPath == "" {
			return indexStep{}, false
		}
		if canReadPath != nil && !canReadPath(change.OldPath) {
			return indexStep{created: true, redacted: true}, true
		}
		prevPath = change.OldPath
	default:
		return indexStep{}, false
	}
	for j := index + 1; j < len(commits); j++ {
		if len(c[commits[j].Id][prevPath]) == 0 {
			return indexStep{}, false
		}
		prev, ok := c.one(commits[j].Id, prevPath)
		if !ok || prev.ContentHash == "" || (prev.ChangeKind != "added" && prev.ChangeKind != "modified" && prev.ChangeKind != "moved") {
			return indexStep{}, false
		}
		return indexStep{parentPath: prevPath, parentHash: prev.ContentHash}, true
	}
	return indexStep{}, false
}

func (c changes) chain(commits []*corev1.Commit, index int, path, hash string, snapshots map[string]map[string]*storage.BlameSnapshot, limit int, canReadPath func(string) bool) []indexStep {
	var out []indexStep
	for i := index; i < len(commits) && len(out) < limit; i++ {
		if canReadPath == nil {
			if snapshot := snapshots[commits[i].Id][path]; snapshot != nil && snapshot.ContentHash == hash {
				break
			}
		}
		if len(commits[i].ParentIds) == 0 {
			out = append(out, indexStep{created: true})
			break
		}
		step, ok := c.step(commits, i, path, hash, canReadPath)
		if !ok {
			break
		}
		out = append(out, step)
		if step.created {
			break
		}
		path, hash = step.parentPath, step.parentHash
	}
	return out
}

// blobPrefetcher reads file contents by hash, several at a time, ahead of the walk.
type blobPrefetcher struct {
	ctx     context.Context
	cancel  context.CancelFunc
	engine  *Engine
	mu      sync.Mutex
	reads   map[string]*blobRead
	sem     chan struct{}
	started int
}

type blobRead struct {
	done  chan struct{}
	lines []string
	err   error
}

const (
	blobConcurrency    = 8
	blobReadAhead      = 64
	blobReadAheadBytes = 32 << 20
)

func newBlobPrefetcher(ctx context.Context, engine *Engine) *blobPrefetcher {
	ctx, cancel := context.WithCancel(ctx)
	return &blobPrefetcher{
		ctx:    ctx,
		cancel: cancel,
		engine: engine,
		reads:  map[string]*blobRead{},
		sem:    make(chan struct{}, blobConcurrency),
	}
}

func (b *blobPrefetcher) prefetch(steps []indexStep, size int64) {
	budget := blobReadAhead
	if size > 0 {
		budget = min(budget, int(max(int64(2), blobReadAheadBytes/size)))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, step := range steps {
		if b.started >= budget {
			return
		}
		if step.created || step.parentHash == "" || b.reads[step.parentHash] != nil {
			continue
		}
		b.start(step.parentHash)
	}
}

func (b *blobPrefetcher) start(hash string) *blobRead {
	read := &blobRead{done: make(chan struct{})}
	b.reads[hash] = read
	b.started++
	go func() {
		defer close(read.done)
		select {
		case b.sem <- struct{}{}:
			defer func() { <-b.sem }()
		case <-b.ctx.Done():
			read.err = b.ctx.Err()
			return
		}
		read.lines, read.err = b.engine.LinesByHash(b.ctx, hash)
	}()
	return read
}

func (b *blobPrefetcher) lines(hash string) ([]string, error) {
	b.mu.Lock()
	read := b.reads[hash]
	if read == nil {
		read = b.start(hash)
	}
	b.mu.Unlock()
	select {
	case <-read.done:
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	}
	b.mu.Lock()
	delete(b.reads, hash)
	b.mu.Unlock()
	return read.lines, read.err
}

func (b *blobPrefetcher) stop() { b.cancel() }
