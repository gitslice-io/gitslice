package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/diffutil"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Blame (design/27_blame.md): which commit last changed each line of a file.
//
// A file's blame at the commit that last changed it is that commit's parent
// version's blame carried through one diff: lines the diff matches keep
// their owner, the rest are the commit's. Blames are stored as snapshots
// (storage.BlameStore), so a blame walks back through the file's history
// (following moves, as path history does) only until it reaches a version
// with a snapshot, or the commit that created the file, and then carries the
// blame forward one diff per newer version. The newest result is stored for
// the next blame. The walk reads at most blameMaxCommits versions.

const (
	blameMaxCommits = 500
	blameMaxBytes   = 4 << 20
	blamePageSize   = 100
	// blameMoveConcurrency is how many candidate paths a move search looks at
	// at once; each is a storage round trip.
	blameMoveConcurrency = 16
)

// blameTrace counts what one blame did and times its stages, for one log line
// (a blame is slow for reasons the request alone does not show).
type blameTrace struct {
	start      time.Time
	last       time.Time
	stages     []any
	entries    atomic.Int64 // file lookups in a commit
	blobs      atomic.Int64 // file contents read
	candidates atomic.Int64 // paths a move search considered
}

type blameTraceKey struct{}

func newBlameTrace() *blameTrace {
	now := time.Now()
	return &blameTrace{start: now, last: now}
}

func blameTraceOf(ctx context.Context) *blameTrace {
	t, _ := ctx.Value(blameTraceKey{}).(*blameTrace)
	return t
}

// mark ends a stage: its duration is logged as name_ms.
func (t *blameTrace) mark(name string) {
	now := time.Now()
	t.stages = append(t.stages, name+"_ms", now.Sub(t.last).Milliseconds())
	t.last = now
}

func (s *RepositoryService) BlameFile(ctx context.Context, req *corev1.BlameFileRequest) (*corev1.BlameFileResponse, error) {
	trace := newBlameTrace()
	ctx = context.WithValue(ctx, blameTraceKey{}, trace)
	var walked *blameResult
	defer func() {
		attrs := []any{"path", req.GetPath(), "total_ms", time.Since(trace.start).Milliseconds()}
		attrs = append(attrs, trace.stages...)
		attrs = append(attrs, "entry_lookups", trace.entries.Load(), "blob_reads", trace.blobs.Load(), "move_candidates", trace.candidates.Load())
		if walked != nil {
			attrs = append(attrs, "lines", len(walked.owners), "walked_commits", walked.walked, "steps", walked.steps, "snapshot_hit", walked.snapshotHit, "truncated", walked.truncated)
		}
		slog.Info("blame", attrs...)
	}()
	subjectID := optionalSubject(ctx)
	p, err := paths.Canonical(req.GetPath())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if err := ensureRepositoryPathInSlice(slice, p); err != nil {
			return nil, err
		}
	} else if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
		return nil, err
	}
	refName := strings.TrimSpace(req.GetRefName())
	if err := requireDefaultTargetRef(refName); err != nil {
		return nil, err
	}
	if refName == "" {
		refName = storage.DefaultTargetRef
	}
	trace.mark("access")
	// The head and the history filters do not depend on each other.
	var head *corev1.Ref
	var headEntry *storage.FileEntry
	var filters listCommitFilters
	var group errgroup.Group
	group.Go(func() error {
		var err error
		if head, err = s.Repository.GetRef(ctx, refName); err != nil {
			return grpcError(err)
		}
		var found bool
		if headEntry, found, err = s.blameEntry(ctx, head.CommitId, p); err != nil {
			return err
		}
		if !found {
			return status.Errorf(codes.NotFound, "no file at %s", p)
		}
		return nil
	})
	group.Go(func() error {
		var err error
		filters, err = s.listCommitFilters(ctx, subjectID, &corev1.ListCommitsRequest{RefName: refName, Path: p, Slice: req.Slice})
		return err
	})
	if err := group.Wait(); err != nil {
		return nil, err
	}
	trace.mark("head")

	result, err := s.blameWalk(ctx, refName, filters, head.CommitId, p, headEntry)
	if err != nil {
		return nil, err
	}
	walked = &result
	trace.mark("walk")

	out := &corev1.BlameFileResponse{CommitId: head.CommitId, Path: p, LineCount: int32(len(result.owners)), Truncated: result.truncated}
	var ids []string
	named := map[string]bool{}
	for start := 0; start < len(result.owners); {
		end := start
		for end < len(result.owners) && result.owners[end] == result.owners[start] {
			end++
		}
		owner := result.owners[start]
		out.Ranges = append(out.Ranges, &corev1.BlameRange{StartLine: int32(start + 1), LineCount: int32(end - start), CommitId: owner})
		if !named[owner] {
			named[owner] = true
			ids = append(ids, owner)
		}
		start = end
	}
	// Commits a stored snapshot names, from beyond this walk, in one query.
	var missing []string
	for _, id := range ids {
		if result.commits[id] == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		read, err := s.Repository.GetCommits(ctx, missing)
		if err != nil {
			return nil, grpcError(err)
		}
		for _, id := range missing {
			if read[id] == nil {
				return nil, grpcError(storage.ErrNotFound)
			}
			result.commits[id] = read[id]
		}
	}
	for _, id := range ids {
		out.Commits = append(out.Commits, result.commits[id])
	}
	trace.mark("commits")
	if err := s.resolveCommitAuthors(ctx, out.Commits...); err != nil {
		return nil, grpcError(err)
	}
	trace.mark("authors")
	return out, nil
}

type blameResult struct {
	owners    []string
	truncated bool
	// What the walk did, for the log: commits it looked at, steps it carried
	// the blame through, and whether it stopped at a stored blame.
	walked      int
	steps       int
	snapshotHit bool
	// guessed: the walk ran out of listed history before the file's creation
	// (see where snapshots are stored), so nothing was stored.
	guessed bool
	// commits holds the commits the walk read, by id.
	commits map[string]*corev1.Commit
}

// blameStep is one commit of the walk: the file's version right after the
// commit, and right before it (none when the commit created it).
type blameStep struct {
	commit  *corev1.Commit
	path    string
	version blameVersion
	parent  *blameVersion
}

func (s *RepositoryService) blameWalk(ctx context.Context, refName string, filters listCommitFilters, headCommitID, p string, headEntry *storage.FileEntry) (blameResult, error) {
	result := blameResult{commits: map[string]*corev1.Commit{}}
	trackedPath, trackedHash := p, headEntry.ContentHash
	var trackedLines []string
	loaded := false
	var steps []blameStep
	var base *storage.BlameSnapshot
	created := false
	seen, token := 0, ""
	first := true
	capped := false
walk:
	for {
		page, err := s.listCommitPage(ctx, refName, filters, blamePageSize, token)
		if err != nil {
			return result, err
		}
		commits := page.Commits
		if first {
			first = false
			recent, err := s.blameUnlistedCommits(ctx, headCommitID, p, headEntry.ContentHash, commits)
			if err != nil {
				return result, err
			}
			commits = append(recent, commits...)
		}
		snapshots := map[string]map[string]*storage.BlameSnapshot{}
		if s.Blames != nil && len(commits) > 0 {
			ids := make([]string, 0, len(commits))
			for _, commit := range commits {
				ids = append(ids, commit.Id)
			}
			if snapshots, err = s.Blames.BlameSnapshots(ctx, ids); err != nil {
				slog.Warn("blame snapshots unavailable", "error", err)
				snapshots = map[string]map[string]*storage.BlameSnapshot{}
			}
		}
		// Read the versions ahead of the walk, several at a time: the walk
		// needs them in order, but fetching each is a storage round trip.
		prefetch := newBlamePrefetch(ctx, s, commits, func(i int, path string) bool {
			return snapshots[commits[i].Id][path] != nil
		})
		defer prefetch.stop()
		for index, commit := range commits {
			result.commits[commit.Id] = commit
			// A stored blame of exactly the version being tracked ends the walk.
			if snapshot := snapshots[commit.Id][trackedPath]; snapshot != nil && snapshot.ContentHash == trackedHash {
				base = snapshot
				break walk
			}
			if seen++; seen > blameMaxCommits {
				result.truncated = true
				capped = true
				break walk
			}
			if !loaded {
				if trackedLines, err = s.blameLines(ctx, trackedPath, trackedHash, headEntry.Size); err != nil {
					return result, err
				}
				loaded = true
			}
			step := blameStep{commit: commit, path: trackedPath, version: blameVersion{hash: trackedHash, lines: trackedLines}}
			parentPath := trackedPath
			if len(commit.ParentIds) > 0 {
				parent, found, err := prefetch.version(index, trackedPath)
				if err != nil {
					return result, err
				}
				if !found {
					// Moved in this commit? Read it where it was.
					if parentPath, err = s.blameMovedFrom(ctx, commit, trackedPath, step.version); err != nil {
						return result, err
					}
					if parentPath != "" {
						if parent, found, err = s.blameVersion(ctx, commit.ParentIds[0], parentPath); err != nil {
							return result, err
						}
					}
				}
				if found {
					step.parent = &parent
				}
			}
			steps = append(steps, step)
			if step.parent == nil {
				created = true
				break walk
			}
			trackedPath, trackedHash, trackedLines = parentPath, step.parent.hash, step.parent.lines
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}

	result.walked, result.steps, result.snapshotHit = seen, len(steps), base != nil

	// The blame of the version just before the oldest step, then carried
	// forward through each step.
	var owners []string
	switch {
	case base != nil:
		owners = base.Owners
		result.truncated = base.Truncated
	case created:
		// The oldest step created the file; it starts the carry below.
	case len(steps) > 0:
		// The walk stopped (a limit, or history that ends without creating
		// the file): what is older is credited to the oldest commit read.
		oldest := steps[len(steps)-1]
		owners = make([]string, len(oldest.parent.lines))
		for i := range owners {
			owners[i] = oldest.commit.Id
		}
		result.truncated = true
	default:
		// No recorded history for the path: the head commit is all there is.
		lines, err := s.blameLines(ctx, p, headEntry.ContentHash, headEntry.Size)
		if err != nil {
			return result, err
		}
		owners = make([]string, len(lines))
		for i := range owners {
			owners[i] = headCommitID
		}
		result.owners = owners
		return result, nil
	}
	for i := len(steps) - 1; i >= 0; i-- {
		owners = carryBlame(owners, steps[i])
	}
	result.owners = owners

	// Store what was computed for the next blame: the blame of the version
	// the newest step left, which is the head's, unless history listing has
	// not caught up with the head yet. A walk that ran out of listed history
	// without reaching the file's creation or a stored blame (and did not
	// stop at the commit limit) credited what is older to the oldest commit
	// it read: that is a guess, often because history is indexed shortly
	// after a commit lands, and storing it would keep it wrong for good.
	guessed := base == nil && !created && !capped
	result.guessed = guessed && len(steps) > 0
	if s.Blames != nil && len(steps) > 0 && !guessed {
		newest := steps[0]
		if entry, found, err := s.blameEntry(ctx, newest.commit.Id, newest.path); err == nil && found && entry.ContentHash == newest.version.hash {
			if err := s.Blames.PutBlameSnapshot(ctx, storage.BlameSnapshot{
				CommitID:    newest.commit.Id,
				Path:        newest.path,
				ContentHash: newest.version.hash,
				Truncated:   result.truncated,
				Owners:      owners,
			}); err != nil {
				slog.Warn("storing blame snapshot failed", "commit_id", newest.commit.Id, "path", newest.path, "error", err)
			}
		}
	}
	return result, nil
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

type blameVersion struct {
	hash  string
	lines []string
}

// blameEntry finds the file at path in a commit; found is false when there
// is none.
func (s *RepositoryService) blameEntry(ctx context.Context, commitID, p string) (*storage.FileEntry, bool, error) {
	if t := blameTraceOf(ctx); t != nil {
		t.entries.Add(1)
	}
	entry, err := s.Repository.GetFile(ctx, commitID, p)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, grpcError(err)
	}
	if entry.Size > blameMaxBytes {
		return nil, false, status.Errorf(codes.FailedPrecondition, "%s is larger than %d bytes, too large to blame", p, blameMaxBytes)
	}
	return entry, true, nil
}

// blameLines reads a file version's lines, refusing binary files.
func (s *RepositoryService) blameLines(ctx context.Context, p, contentHash string, size int64) ([]string, error) {
	if size > blameMaxBytes {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is larger than %d bytes, too large to blame", p, blameMaxBytes)
	}
	if t := blameTraceOf(ctx); t != nil {
		t.blobs.Add(1)
	}
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, grpcError(err)
	}
	data := buf.Bytes()
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s is a binary file", p)
	}
	return splitBlameLines(data), nil
}

// blameVersion reads the file at path in a commit; found is false when there
// is no file there.
func (s *RepositoryService) blameVersion(ctx context.Context, commitID, p string) (blameVersion, bool, error) {
	entry, found, err := s.blameEntry(ctx, commitID, p)
	if err != nil || !found {
		return blameVersion{}, found, err
	}
	lines, err := s.blameLines(ctx, p, entry.ContentHash, entry.Size)
	if err != nil {
		return blameVersion{}, false, err
	}
	return blameVersion{hash: entry.ContentHash, lines: lines}, true, nil
}

// blameMovedFrom finds where a file was before a commit that moved it: among
// the paths the commit changed, one that had a file in the parent and none
// after, choosing the one sharing the most lines with the file.
func (s *RepositoryService) blameMovedFrom(ctx context.Context, commit *corev1.Commit, movedTo string, moved blameVersion) (string, error) {
	if len(commit.ParentIds) == 0 {
		return "", nil
	}
	have := map[string]int{}
	for _, line := range moved.lines {
		have[line]++
	}
	// A commit that created many files lists each of them among its changed
	// paths, and none was where this file came from: they are not in the
	// parent. Looking in the parent first makes those one lookup, not two,
	// and the lookups run several at a time.
	var candidates []string
	for _, changed := range commit.ChangedPaths {
		if candidate, err := paths.Canonical(changed); err == nil && candidate != movedTo {
			candidates = append(candidates, candidate)
		}
	}
	if t := blameTraceOf(ctx); t != nil {
		t.candidates.Add(int64(len(candidates)))
	}
	shared := make([]int, len(candidates))
	for i := range shared {
		shared[i] = -1
	}
	var group errgroup.Group
	group.SetLimit(blameMoveConcurrency)
	for i, candidate := range candidates {
		group.Go(func() error {
			parentEntry, found, err := s.blameEntry(ctx, commit.ParentIds[0], candidate)
			if err != nil || !found {
				return nil
			}
			if _, stillThere, err := s.blameEntry(ctx, commit.Id, candidate); err != nil || stillThere {
				return nil
			}
			lines, err := s.blameLines(ctx, candidate, parentEntry.ContentHash, parentEntry.Size)
			if err != nil {
				return nil
			}
			n, counts := 0, map[string]int{}
			for _, line := range lines {
				if counts[line] < have[line] {
					counts[line]++
					n++
				}
			}
			shared[i] = n
			return nil
		})
	}
	_ = group.Wait()
	// The first of the best, as a sequential search would pick.
	best, bestShared := "", -1
	for i, candidate := range candidates {
		if shared[i] > bestShared {
			best, bestShared = candidate, shared[i]
		}
	}
	return best, nil
}

func splitBlameLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
