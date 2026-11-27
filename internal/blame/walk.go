package blame

import (
	"context"
	"log/slog"

	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
)

// WalkState holds mutable state across commit pages of a blame walk.
type WalkState struct {
	RefName      string
	Filters      CommitFilters
	HeadCommitID string
	HeadPath     string
	HeadEntry    *storage.FileEntry
	CanReadPath  func(string) bool
	PageSize     int

	result       Result
	trackedPath  string
	trackedHash  string
	trackedLines []string
	loaded       bool
	steps        []Step
	base         *storage.BlameSnapshot
	created      bool
	redactedMove bool
	seen         int
	first        bool
	capped       bool

	// OnPageContexts is an optional test hook observing each page's read-ahead
	// contexts so tests can assert per-page cancellation on return.
	OnPageContexts func(blobsCtx, prefetchCtx context.Context)
}

// Walk computes per-line commit attribution for headPath at headCommitID.
func (e *Engine) Walk(ctx context.Context, refName string, filters CommitFilters, headCommitID, p string, headEntry *storage.FileEntry, canReadPath func(string) bool) (Result, error) {
	return e.WalkWithState(ctx, &WalkState{
		RefName:      refName,
		Filters:      filters,
		HeadCommitID: headCommitID,
		HeadPath:     p,
		HeadEntry:    headEntry,
		CanReadPath:  canReadPath,
		PageSize:     PageSize,
	})
}

// WalkWithState runs a blame walk using caller-provided state (e.g. custom page
// size or test hooks).
func (e *Engine) WalkWithState(ctx context.Context, st *WalkState) (Result, error) {
	if st.PageSize <= 0 {
		st.PageSize = PageSize
	}
	if st.result.Commits == nil {
		st.result.Commits = map[string]*corev1.Commit{}
	}
	if st.trackedPath == "" {
		st.trackedPath = st.HeadPath
	}
	if st.trackedHash == "" && st.HeadEntry != nil {
		st.trackedHash = st.HeadEntry.ContentHash
	}
	st.first = true

	token := ""
	for {
		nextToken, done, err := e.walkPage(ctx, st, token)
		if err != nil {
			return st.result, err
		}
		if done {
			break
		}
		token = nextToken
	}

	st.result.Walked, st.result.Steps, st.result.SnapshotHit = st.seen, len(st.steps), st.base != nil

	var owners []string
	switch {
	case st.base != nil:
		owners = st.base.Owners
		st.result.Truncated = st.base.Truncated
	case st.created:
		// The oldest step created the file; it starts the carry below.
	case len(st.steps) > 0:
		oldest := st.steps[len(st.steps)-1]
		owners = make([]string, len(oldest.Parent.Lines))
		for i := range owners {
			owners[i] = oldest.Commit.Id
		}
		st.result.Truncated = true
	default:
		lines, err := e.Lines(ctx, st.HeadPath, st.HeadEntry.ContentHash, st.HeadEntry.Size)
		if err != nil {
			return st.result, err
		}
		owners = make([]string, len(lines))
		for i := range owners {
			owners[i] = st.HeadCommitID
		}
		st.result.Owners = owners
		return st.result, nil
	}
	for i := len(st.steps) - 1; i >= 0; i-- {
		owners = Carry(owners, st.steps[i])
	}
	st.result.Owners = owners

	guessed := st.base == nil && !st.created && !st.capped
	st.result.Guessed = guessed && len(st.steps) > 0
	if e.Blames != nil && len(st.steps) > 0 && !guessed && !st.redactedMove {
		newest := st.steps[0]
		if entry, found, err := e.Entry(ctx, newest.Commit.Id, newest.Path); err == nil && found && entry.ContentHash == newest.Version.Hash {
			if err := e.Blames.PutBlameSnapshot(ctx, storage.BlameSnapshot{
				CommitID:    newest.Commit.Id,
				Path:        newest.Path,
				ContentHash: newest.Version.Hash,
				Truncated:   st.result.Truncated,
				Owners:      owners,
			}); err != nil {
				slog.Warn("storing blame snapshot failed", "commit_id", newest.Commit.Id, "path", newest.Path, "error", err)
			}
		}
	}
	return st.result, nil
}

func (e *Engine) walkPage(ctx context.Context, st *WalkState, token string) (string, bool, error) {
	page, err := e.ListCommitPage(ctx, st.RefName, st.Filters, st.PageSize, token)
	if err != nil {
		return "", false, err
	}
	commits := page.Commits
	if st.first {
		st.first = false
		recent, err := e.unlistedCommits(ctx, st.HeadCommitID, st.HeadPath, st.HeadEntry.ContentHash, commits)
		if err != nil {
			return "", false, err
		}
		commits = append(recent, commits...)
	}
	snapshots := map[string]map[string]*storage.BlameSnapshot{}
	pageChanges := changes{}
	if len(commits) > 0 {
		ids := make([]string, 0, len(commits))
		for _, commit := range commits {
			ids = append(ids, commit.Id)
		}
		var group errgroup.Group
		if e.Blames != nil {
			group.Go(func() error {
				found, err := e.Blames.BlameSnapshots(ctx, ids)
				if err != nil {
					slog.Warn("blame snapshots unavailable", "error", err)
					return nil
				}
				snapshots = found
				return nil
			})
		}
		group.Go(func() error {
			list, err := e.Repository.FileChanges(ctx, st.RefName, ids)
			if err != nil {
				slog.Warn("blame file changes unavailable", "error", err)
				return nil
			}
			pageChanges = newChanges(list)
			return nil
		})
		_ = group.Wait()
	}

	blobs := newBlobPrefetcher(ctx, e)
	defer blobs.stop()
	var plan []indexStep
	planFrom := -1
	prefetch := newVersionPrefetcher(ctx, e, commits, func(i int, path string) bool {
		return snapshots[commits[i].Id][path] != nil
	})
	defer prefetch.stop()
	if st.OnPageContexts != nil {
		st.OnPageContexts(blobs.ctx, prefetch.ctx)
	}

	for index, commit := range commits {
		st.result.Commits[commit.Id] = commit
		if snapshot := snapshots[commit.Id][st.trackedPath]; snapshot != nil && snapshot.ContentHash == st.trackedHash {
			if e.SnapshotReadable(ctx, snapshot, st.result.Commits, st.CanReadPath) {
				st.base = snapshot
				return "", true, nil
			}
		}
		if st.seen++; st.seen > MaxCommits {
			st.result.Truncated = true
			st.capped = true
			return "", true, nil
		}
		if !st.loaded {
			if st.trackedLines, err = e.Lines(ctx, st.trackedPath, st.trackedHash, st.HeadEntry.Size); err != nil {
				return "", false, err
			}
			st.loaded = true
		}
		step := Step{Commit: commit, Path: st.trackedPath, Version: Version{Hash: st.trackedHash, Lines: st.trackedLines}}
		parentPath := st.trackedPath

		var planned *indexStep
		if len(commit.ParentIds) > 0 && len(pageChanges) > 0 && fromIndex.Load() {
			if planFrom < 0 || index < planFrom || index-planFrom >= len(plan) {
				plan = pageChanges.chain(commits, index, st.trackedPath, st.trackedHash, snapshots, blobReadAhead, st.CanReadPath)
				planFrom = index
				blobs.prefetch(plan, st.HeadEntry.Size)
			}
			if index-planFrom < len(plan) {
				planned = &plan[index-planFrom]
			}
		}
		if planned != nil && planned.created {
			if planned.redacted {
				st.redactedMove = true
			}
			if t := TraceFrom(ctx); t != nil {
				t.IndexSteps.Add(1)
			}
		} else if planned != nil {
			if lines, err := blobs.lines(planned.parentHash); err == nil {
				step.Parent = &Version{Hash: planned.parentHash, Lines: lines}
				parentPath = planned.parentPath
				if t := TraceFrom(ctx); t != nil {
					t.IndexSteps.Add(1)
				}
			} else {
				planned = nil
			}
		}
		if len(commit.ParentIds) > 0 && planned == nil {
			if t := TraceFrom(ctx); t != nil {
				t.TreeSteps.Add(1)
			}
			parent, found, err := prefetch.version(index, st.trackedPath)
			if err != nil {
				return "", false, err
			}
			if !found {
				var moveRedacted bool
				if parentPath, moveRedacted, err = e.MovedFrom(ctx, commit, st.trackedPath, step.Version, st.CanReadPath); err != nil {
					return "", false, err
				}
				if moveRedacted {
					st.redactedMove = true
				}
				if parentPath != "" {
					if parent, found, err = e.VersionAt(ctx, commit.ParentIds[0], parentPath); err != nil {
						return "", false, err
					}
				}
			}
			if found {
				step.Parent = &parent
			}
		}
		st.steps = append(st.steps, step)
		if step.Parent == nil {
			st.created = true
			return "", true, nil
		}
		if parentPath != st.trackedPath && st.CanReadPath != nil && !st.CanReadPath(parentPath) {
			st.redactedMove = true
			st.steps[len(st.steps)-1].Parent = nil
			st.created = true
			return "", true, nil
		}
		st.trackedPath, st.trackedHash, st.trackedLines = parentPath, step.Parent.Hash, step.Parent.Lines
	}
	if page.NextPageToken == "" {
		return "", true, nil
	}
	return page.NextPageToken, false, nil
}

const prefetchWindow = 16

type versionPrefetcher struct {
	ctx     context.Context
	cancel  context.CancelFunc
	engine  *Engine
	commits []*corev1.Commit
	reads   map[int]*versionRead
	mayStop func(i int, path string) bool
}

type versionRead struct {
	path    string
	done    chan struct{}
	version Version
	found   bool
	err     error
}

func newVersionPrefetcher(ctx context.Context, engine *Engine, commits []*corev1.Commit, mayStop func(i int, path string) bool) *versionPrefetcher {
	ctx, cancel := context.WithCancel(ctx)
	return &versionPrefetcher{ctx: ctx, cancel: cancel, engine: engine, commits: commits, reads: map[int]*versionRead{}, mayStop: mayStop}
}

func (p *versionPrefetcher) version(index int, path string) (Version, bool, error) {
	for i := index; i < min(index+prefetchWindow, len(p.commits)); i++ {
		if i > index && p.mayStop(i, path) {
			break
		}
		if read := p.reads[i]; read == nil || read.path != path {
			p.start(i, path)
		}
	}
	read := p.reads[index]
	delete(p.reads, index)
	select {
	case <-read.done:
	case <-p.ctx.Done():
		return Version{}, false, p.ctx.Err()
	}
	return read.version, read.found, read.err
}

func (p *versionPrefetcher) start(i int, path string) {
	read := &versionRead{path: path, done: make(chan struct{})}
	p.reads[i] = read
	commit := p.commits[i]
	go func() {
		defer close(read.done)
		if len(commit.ParentIds) == 0 {
			return
		}
		read.version, read.found, read.err = p.engine.VersionAt(p.ctx, commit.ParentIds[0], path)
	}()
}

func (p *versionPrefetcher) stop() { p.cancel() }

const unlistedMax = 100

func (e *Engine) unlistedCommits(ctx context.Context, headCommitID, p, headHash string, listed []*corev1.Commit) ([]*corev1.Commit, error) {
	listedHash := ""
	if len(listed) > 0 {
		entry, found, err := e.Entry(ctx, listed[0].Id, p)
		if err != nil {
			return nil, err
		}
		if found {
			listedHash = entry.ContentHash
		}
		if listedHash == headHash {
			return nil, nil
		}
	}
	var recent []*corev1.Commit
	commitID, hash := headCommitID, headHash
	for i := 0; i < unlistedMax; i++ {
		if len(listed) > 0 && commitID == listed[0].Id {
			break
		}
		commit, err := e.Repository.GetCommit(ctx, commitID)
		if err != nil {
			return nil, err
		}
		if len(commit.ParentIds) == 0 {
			break
		}
		parentHash := ""
		entry, found, err := e.Entry(ctx, commit.ParentIds[0], p)
		if err != nil {
			return nil, err
		}
		if found {
			parentHash = entry.ContentHash
		}
		if parentHash != hash {
			recent = append(recent, commit)
		}
		if parentHash == listedHash {
			break
		}
		commitID, hash = commit.ParentIds[0], parentHash
	}
	return recent, nil
}

// Carry takes the blame of a step's parent version (owners, one per line) to
// the step's own version: matched lines keep their owner, the rest are the
// step's commit's. A step that created the file owns every line.
func Carry(owners []string, step Step) []string {
	out := make([]string, len(step.Version.Lines))
	if step.Parent == nil {
		for i := range out {
			out[i] = step.Commit.Id
		}
		return out
	}
	if step.Parent.Hash == step.Version.Hash {
		copy(out, owners)
		return out
	}
	match := diffutil.MatchLines(step.Parent.Lines, step.Version.Lines, 0)
	for j, i := range match {
		if i >= 0 && i < len(owners) {
			out[j] = owners[i]
		} else {
			out[j] = step.Commit.Id
		}
	}
	return out
}
