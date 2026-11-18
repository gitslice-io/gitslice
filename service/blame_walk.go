package service

import (
	"context"
	"log/slog"

	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
)

type blameWalkState struct {
	refName      string
	filters      listCommitFilters
	headCommitID string
	headPath     string
	headEntry    *storage.FileEntry
	canReadPath  func(string) bool
	pageSize     int

	result       blameResult
	trackedPath  string
	trackedHash  string
	trackedLines []string
	loaded       bool
	steps        []blameStep
	base         *storage.BlameSnapshot
	created      bool
	redactedMove bool
	seen         int
	first        bool
	capped       bool

	// onPageContexts is an optional test hook observing each page's read-ahead
	// contexts so tests can assert per-page cancellation on return.
	onPageContexts func(blobsCtx, prefetchCtx context.Context)
}

func (s *RepositoryService) blameWalk(ctx context.Context, refName string, filters listCommitFilters, headCommitID, p string, headEntry *storage.FileEntry, canReadPath func(string) bool) (blameResult, error) {
	return s.blameWalkWithState(ctx, &blameWalkState{
		refName:      refName,
		filters:      filters,
		headCommitID: headCommitID,
		headPath:     p,
		headEntry:    headEntry,
		canReadPath:  canReadPath,
		pageSize:     blamePageSize,
		result:       blameResult{commits: map[string]*corev1.Commit{}},
		trackedPath:  p,
		trackedHash:  headEntry.ContentHash,
		first:        true,
	})
}

func (s *RepositoryService) blameWalkWithState(ctx context.Context, st *blameWalkState) (blameResult, error) {
	if st.pageSize <= 0 {
		st.pageSize = blamePageSize
	}
	token := ""
	for {
		nextToken, done, err := s.blameWalkPage(ctx, st, token)
		if err != nil {
			return st.result, err
		}
		if done {
			break
		}
		token = nextToken
	}

	st.result.walked, st.result.steps, st.result.snapshotHit = st.seen, len(st.steps), st.base != nil

	// The blame of the version just before the oldest step, then carried
	// forward through each step.
	var owners []string
	switch {
	case st.base != nil:
		owners = st.base.Owners
		st.result.truncated = st.base.Truncated
	case st.created:
		// The oldest step created the file; it starts the carry below.
	case len(st.steps) > 0:
		// The walk stopped (a limit, or history that ends without creating
		// the file): what is older is credited to the oldest commit read.
		oldest := st.steps[len(st.steps)-1]
		owners = make([]string, len(oldest.parent.lines))
		for i := range owners {
			owners[i] = oldest.commit.Id
		}
		st.result.truncated = true
	default:
		// No recorded history for the path: the head commit is all there is.
		lines, err := s.blameLines(ctx, st.headPath, st.headEntry.ContentHash, st.headEntry.Size)
		if err != nil {
			return st.result, err
		}
		owners = make([]string, len(lines))
		for i := range owners {
			owners[i] = st.headCommitID
		}
		st.result.owners = owners
		return st.result, nil
	}
	for i := len(st.steps) - 1; i >= 0; i-- {
		owners = carryBlame(owners, st.steps[i])
	}
	st.result.owners = owners

	// Store what was computed for the next blame: the blame of the version
	// the newest step left, which is the head's, unless history listing has
	// not caught up with the head yet. A walk that ran out of listed history
	// without reaching the file's creation or a stored blame (and did not
	// stop at the commit limit) credited what is older to the oldest commit
	// it read: that is a guess, often because history is indexed shortly
	// after a commit lands, and storing it would keep it wrong for good.
	guessed := st.base == nil && !st.created && !st.capped
	st.result.guessed = guessed && len(st.steps) > 0
	if s.Blames != nil && len(st.steps) > 0 && !guessed && !st.redactedMove {
		newest := st.steps[0]
		if entry, found, err := s.blameEntry(ctx, newest.commit.Id, newest.path); err == nil && found && entry.ContentHash == newest.version.hash {
			if err := s.Blames.PutBlameSnapshot(ctx, storage.BlameSnapshot{
				CommitID:    newest.commit.Id,
				Path:        newest.path,
				ContentHash: newest.version.hash,
				Truncated:   st.result.truncated,
				Owners:      owners,
			}); err != nil {
				slog.Warn("storing blame snapshot failed", "commit_id", newest.commit.Id, "path", newest.path, "error", err)
			}
		}
	}
	return st.result, nil
}

// blameWalkPage processes one commit page of blameWalk and stops its blob and
// parent-tree prefetchers via function-scoped defers before the next page begins.
func (s *RepositoryService) blameWalkPage(ctx context.Context, st *blameWalkState, token string) (string, bool, error) {
	page, err := s.listCommitPage(ctx, st.refName, st.filters, st.pageSize, token)
	if err != nil {
		return "", false, err
	}
	commits := page.Commits
	if st.first {
		st.first = false
		recent, err := s.blameUnlistedCommits(ctx, st.headCommitID, st.headPath, st.headEntry.ContentHash, commits)
		if err != nil {
			return "", false, err
		}
		commits = append(recent, commits...)
	}
	snapshots := map[string]map[string]*storage.BlameSnapshot{}
	changes := blameChanges{}
	if len(commits) > 0 {
		ids := make([]string, 0, len(commits))
		for _, commit := range commits {
			ids = append(ids, commit.Id)
		}
		// Stored blames and the history index's file changes, together.
		var group errgroup.Group
		if s.Blames != nil {
			group.Go(func() error {
				found, err := s.Blames.BlameSnapshots(ctx, ids)
				if err != nil {
					slog.Warn("blame snapshots unavailable", "error", err)
					return nil
				}
				snapshots = found
				return nil
			})
		}
		group.Go(func() error {
			list, err := s.Repository.FileChanges(ctx, st.refName, ids)
			if err != nil {
				slog.Warn("blame file changes unavailable", "error", err)
				return nil
			}
			changes = newBlameChanges(list)
			return nil
		})
		_ = group.Wait()
	}
	// Contents read ahead from the plan the index gives.
	blobs := newBlameBlobs(ctx, s)
	defer blobs.stop()
	var plan []blameIndexStep
	planFrom := -1
	// Read the versions ahead of the walk, several at a time: the walk
	// needs them in order, but fetching each is a storage round trip.
	prefetch := newBlamePrefetch(ctx, s, commits, func(i int, path string) bool {
		return snapshots[commits[i].Id][path] != nil
	})
	defer prefetch.stop()
	if st.onPageContexts != nil {
		st.onPageContexts(blobs.ctx, prefetch.ctx)
	}

	for index, commit := range commits {
		st.result.commits[commit.Id] = commit
		// A stored blame of exactly the version being tracked ends the walk
		// when every commit it credits is readable by the caller.
		if snapshot := snapshots[commit.Id][st.trackedPath]; snapshot != nil && snapshot.ContentHash == st.trackedHash {
			if s.blameSnapshotReadable(ctx, snapshot, st.result.commits, st.canReadPath) {
				st.base = snapshot
				return "", true, nil
			}
		}
		if st.seen++; st.seen > blameMaxCommits {
			st.result.truncated = true
			st.capped = true
			return "", true, nil
		}
		if !st.loaded {
			if st.trackedLines, err = s.blameLines(ctx, st.trackedPath, st.trackedHash, st.headEntry.Size); err != nil {
				return "", false, err
			}
			st.loaded = true
		}
		step := blameStep{commit: commit, path: st.trackedPath, version: blameVersion{hash: st.trackedHash, lines: st.trackedLines}}
		parentPath := st.trackedPath
		// The version before this commit, from the history index when it
		// is certain (a content read), else from the tree.
		var planned *blameIndexStep
		if len(commit.ParentIds) > 0 && len(changes) > 0 && blameFromIndex.Load() {
			if planFrom < 0 || index < planFrom || index-planFrom >= len(plan) {
				plan = changes.chain(commits, index, st.trackedPath, st.trackedHash, snapshots, blameBlobReadAhead, st.canReadPath)
				planFrom = index
				blobs.prefetch(plan, st.headEntry.Size)
			}
			if index-planFrom < len(plan) {
				planned = &plan[index-planFrom]
			}
		}
		if planned != nil && planned.created {
			if planned.redacted {
				st.redactedMove = true
			}
			if t := blameTraceOf(ctx); t != nil {
				t.indexSteps.Add(1)
			}
		} else if planned != nil {
			if lines, err := blobs.lines(planned.parentHash); err == nil {
				step.parent = &blameVersion{hash: planned.parentHash, lines: lines}
				parentPath = planned.parentPath
				if t := blameTraceOf(ctx); t != nil {
					t.indexSteps.Add(1)
				}
			} else {
				planned = nil
			}
		}
		if len(commit.ParentIds) > 0 && planned == nil {
			if t := blameTraceOf(ctx); t != nil {
				t.treeSteps.Add(1)
			}
			parent, found, err := prefetch.version(index, st.trackedPath)
			if err != nil {
				return "", false, err
			}
			if !found {
				// Moved in this commit? Read it where it was.
				var moveRedacted bool
				if parentPath, moveRedacted, err = s.blameMovedFrom(ctx, commit, st.trackedPath, step.version, st.canReadPath); err != nil {
					return "", false, err
				}
				if moveRedacted {
					st.redactedMove = true
				}
				if parentPath != "" {
					if parent, found, err = s.blameVersion(ctx, commit.ParentIds[0], parentPath); err != nil {
						return "", false, err
					}
				}
			}
			if found {
				step.parent = &parent
			}
		}
		st.steps = append(st.steps, step)
		if step.parent == nil {
			st.created = true
			return "", true, nil
		}
		if parentPath != st.trackedPath && st.canReadPath != nil && !st.canReadPath(parentPath) {
			st.redactedMove = true
			st.steps[len(st.steps)-1].parent = nil
			st.created = true
			return "", true, nil
		}
		st.trackedPath, st.trackedHash, st.trackedLines = parentPath, step.parent.hash, step.parent.lines
	}
	if page.NextPageToken == "" {
		return "", true, nil
	}
	return page.NextPageToken, false, nil
}

// blamePrefetchWindow is how many versions are read ahead of the walk.
const blamePrefetchWindow = 16

// blamePrefetch reads the file in the parents of upcoming commits while the
// walk works through earlier ones. Reads are started for the path the walk
// expects; when a move changes the path, the reads ahead are started again.
type blamePrefetch struct {
	ctx     context.Context
	cancel  context.CancelFunc
	s       *RepositoryService
	commits []*corev1.Commit
	reads   map[int]*blameRead
	// mayStop reports a commit the walk will likely stop at (it has a
	// snapshot), which the reads ahead do not go past.
	mayStop func(i int, path string) bool
}

type blameRead struct {
	path    string
	done    chan struct{}
	version blameVersion
	found   bool
	err     error
}

func newBlamePrefetch(ctx context.Context, s *RepositoryService, commits []*corev1.Commit, mayStop func(i int, path string) bool) *blamePrefetch {
	ctx, cancel := context.WithCancel(ctx)
	return &blamePrefetch{ctx: ctx, cancel: cancel, s: s, commits: commits, reads: map[int]*blameRead{}, mayStop: mayStop}
}

// version returns the file at path in the first parent of commits[index],
// starting the reads for the next commits too.
func (p *blamePrefetch) version(index int, path string) (blameVersion, bool, error) {
	for i := index; i < min(index+blamePrefetchWindow, len(p.commits)); i++ {
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
		return blameVersion{}, false, p.ctx.Err()
	}
	return read.version, read.found, read.err
}

func (p *blamePrefetch) start(i int, path string) {
	read := &blameRead{path: path, done: make(chan struct{})}
	p.reads[i] = read
	commit := p.commits[i]
	go func() {
		defer close(read.done)
		if len(commit.ParentIds) == 0 {
			return
		}
		read.version, read.found, read.err = p.s.blameVersion(p.ctx, commit.ParentIds[0], path)
	}()
}

// stop abandons the reads still running.
func (p *blamePrefetch) stop() {
	p.cancel()
}

// blameUnlistedMax bounds the walk along the main line for commits path
// history does not list yet.
const blameUnlistedMax = 100

// blameUnlistedCommits finds the commits that changed the file but are not in
// its path history yet: history is indexed shortly after a commit lands, so a
// blame right after one could otherwise credit its lines to an older commit.
// When the file at the head is not the version the newest listed commit left,
// it walks the main line back from the head, collecting the commits that
// changed the file, until the file is that version again.
func (s *RepositoryService) blameUnlistedCommits(ctx context.Context, headCommitID, p, headHash string, listed []*corev1.Commit) ([]*corev1.Commit, error) {
	listedHash := ""
	if len(listed) > 0 {
		entry, found, err := s.blameEntry(ctx, listed[0].Id, p)
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
	for i := 0; i < blameUnlistedMax; i++ {
		if len(listed) > 0 && commitID == listed[0].Id {
			break
		}
		commit, err := s.Repository.GetCommit(ctx, commitID)
		if err != nil {
			return nil, grpcError(err)
		}
		if len(commit.ParentIds) == 0 {
			break
		}
		parentHash := ""
		entry, found, err := s.blameEntry(ctx, commit.ParentIds[0], p)
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

// carryBlame takes the blame of a step's parent version (owners, one per
// line) to the step's own version: matched lines keep their owner, the rest
// are the step's commit's. A step that created the file owns every line.
func carryBlame(owners []string, step blameStep) []string {
	out := make([]string, len(step.version.lines))
	if step.parent == nil {
		for i := range out {
			out[i] = step.commit.Id
		}
		return out
	}
	if step.parent.hash == step.version.hash {
		copy(out, owners)
		return out
	}
	match := diffutil.MatchLines(step.parent.lines, step.version.lines, 0)
	for j, i := range match {
		if i >= 0 && i < len(owners) {
			out[j] = owners[i]
		} else {
			out[j] = step.commit.Id
		}
	}
	return out
}

// listCommitPage reads one page of commits for the filters, as ListCommits
// does.
func (s *RepositoryService) listCommitPage(ctx context.Context, refName string, filters listCommitFilters, limit int, token string) (*storage.CommitListPage, error) {
	var page *storage.CommitListPage
	var err error
	switch {
	case filters.filtered && len(filters.prefixes) == 0 && len(filters.entityRefs) == 0:
		return &storage.CommitListPage{}, nil
	case len(filters.entityRefs) > 0 && filters.includePrefixesWithEntities:
		page, err = s.Repository.ListCommitPageByEntityRefsOrPathPrefixes(ctx, refName, filters.entityRefs, filters.prefixes, limit, token)
	case len(filters.entityRefs) > 0:
		page, err = s.Repository.ListCommitPageByEntityRefs(ctx, refName, filters.entityRefs, limit, token)
	case len(filters.prefixes) == 0:
		page, err = s.Repository.ListCommitPage(ctx, refName, limit, token)
	default:
		page, err = s.Repository.ListCommitPageByPathPrefixes(ctx, refName, filters.prefixes, limit, token)
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return page, nil
}
