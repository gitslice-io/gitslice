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
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

// blameFromIndex lets a blame plan its steps from the history index. A test
// turns it off to compare with reading trees.
var blameFromIndex atomic.Bool

func init() { blameFromIndex.Store(true) }

// SetBlameFromIndex turns planning blame steps from the history index on or
// off (on by default); for tests.
func SetBlameFromIndex(on bool) { blameFromIndex.Store(on) }

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
	indexSteps atomic.Int64 // steps planned from the history index
	treeSteps  atomic.Int64 // steps that read the commit's tree
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
		attrs = append(attrs, "entry_lookups", trace.entries.Load(), "blob_reads", trace.blobs.Load(), "move_candidates", trace.candidates.Load(), "index_steps", trace.indexSteps.Load(), "tree_steps", trace.treeSteps.Load())
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
	var authorizedSlice *corev1.Slice
	if req.Slice != nil {
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		if err := ensureRepositoryPathInSlice(slice, p); err != nil {
			return nil, err
		}
		authorizedSlice = slice
	} else if err := s.ensureRepositoryPathRead(ctx, subjectID, p); err != nil {
		return nil, err
	}
	pathAllowedCache := map[string]bool{p: true}
	canReadPath := func(candidate string) bool {
		canonical, err := repositoryReadPath(candidate)
		if err != nil || canonical == "/" {
			return false
		}
		if allowed, seen := pathAllowedCache[canonical]; seen {
			return allowed
		}
		allowed := false
		if authorizedSlice != nil && ensureRepositoryPathInSlice(authorizedSlice, canonical) == nil {
			allowed = true
		} else if s.ensureRepositoryPathRead(ctx, subjectID, canonical) == nil {
			allowed = true
		}
		pathAllowedCache[canonical] = allowed
		return allowed
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

	result, err := s.blameWalk(ctx, refName, filters, head.CommitId, p, headEntry, canReadPath)
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
		cloned := proto.Clone(result.commits[id]).(*corev1.Commit)
		filtered := cloned.ChangedPaths[:0]
		for _, changed := range cloned.ChangedPaths {
			if canReadPath(changed) {
				filtered = append(filtered, changed)
			}
		}
		cloned.ChangedPaths = filtered
		out.Commits = append(out.Commits, cloned)
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
func (s *RepositoryService) blameMovedFrom(ctx context.Context, commit *corev1.Commit, movedTo string, moved blameVersion, canReadPath func(string) bool) (string, bool, error) {
	if len(commit.ParentIds) == 0 {
		return "", false, nil
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
	redacted := false
	for _, changed := range commit.ChangedPaths {
		if candidate, err := paths.Canonical(changed); err == nil && candidate != movedTo {
			if canReadPath != nil && !canReadPath(candidate) {
				redacted = true
				continue
			}
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
	return best, redacted && best == "", nil
}

func (s *RepositoryService) blameSnapshotReadable(ctx context.Context, snapshot *storage.BlameSnapshot, known map[string]*corev1.Commit, canReadPath func(string) bool) bool {
	if canReadPath == nil || snapshot == nil {
		return true
	}
	seen := map[string]bool{}
	var missing []string
	for _, owner := range snapshot.Owners {
		if seen[owner] {
			continue
		}
		seen[owner] = true
		if known[owner] == nil {
			missing = append(missing, owner)
		}
	}
	if len(missing) > 0 {
		read, err := s.Repository.GetCommits(ctx, missing)
		if err != nil {
			return false
		}
		for _, id := range missing {
			if read[id] == nil {
				return false
			}
			known[id] = read[id]
		}
	}
	for owner := range seen {
		commit := known[owner]
		if commit == nil {
			return false
		}
		hasReadable := false
		for _, changed := range commit.ChangedPaths {
			if canReadPath(changed) {
				hasReadable = true
				break
			}
		}
		if !hasReadable {
			return false
		}
	}
	return true
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
