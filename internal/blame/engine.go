// Package blame computes per-line commit attribution for files by walking path
// history, consulting stored blame snapshots, and carrying ownership forward
// across diffs (design/27_blame.md).
package blame

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
)

var (
	// ErrTooLarge reports that a file version exceeds MaxBytes and cannot be blamed.
	ErrTooLarge = errors.New("file too large to blame")
	// ErrBinary reports that a file version contains binary NUL bytes.
	ErrBinary = errors.New("binary file")
)

var fromIndex atomic.Bool

func init() { fromIndex.Store(true) }

// SetFromIndex turns planning blame steps from the history index on or off (on
// by default); for tests.
func SetFromIndex(on bool) { fromIndex.Store(on) }

// FromIndex reports whether history-index step planning is enabled.
func FromIndex() bool { return fromIndex.Load() }

const (
	MaxCommits      = 500
	MaxBytes        = 4 << 20
	PageSize        = 100
	MoveConcurrency = 16
)

// ObjectGetter reads stored blob objects by key.
type ObjectGetter interface {
	Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
}

// Engine executes blame walks using repository history, optional blame
// snapshots, and object storage.
type Engine struct {
	Repository storage.RepositoryStore
	Blames     storage.BlameStore
	Objects    ObjectGetter
}

// CommitFilters scopes commit listing during a blame walk.
type CommitFilters struct {
	Prefixes                    []string
	EntityRefs                  []storage.HistoryEntityRef
	IncludePrefixesWithEntities bool
	Filtered                    bool
}

// Trace counts what one blame did and times its stages for structured logging.
type Trace struct {
	Start      time.Time
	last       time.Time
	stages     []any
	Entries    atomic.Int64 // file lookups in a commit
	Blobs      atomic.Int64 // file contents read
	Candidates atomic.Int64 // paths a move search considered
	IndexSteps atomic.Int64 // steps planned from the history index
	TreeSteps  atomic.Int64 // steps that read the commit's tree
}

type traceKey struct{}

// NewTrace creates a new blame trace starting now.
func NewTrace() *Trace {
	now := time.Now()
	return &Trace{Start: now, last: now}
}

// WithTrace attaches trace to ctx.
func WithTrace(ctx context.Context, trace *Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, trace)
}

// TraceFrom returns the Trace stored in ctx, or nil.
func TraceFrom(ctx context.Context) *Trace {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	return t
}

// Mark ends a stage; its duration is recorded as name_ms.
func (t *Trace) Mark(name string) {
	now := time.Now()
	t.stages = append(t.stages, name+"_ms", now.Sub(t.last).Milliseconds())
	t.last = now
}

// LogAttrs returns structured slog attributes summarizing the trace and result.
func (t *Trace) LogAttrs(path string, walked *Result) []any {
	attrs := []any{"path", path, "total_ms", time.Since(t.Start).Milliseconds()}
	attrs = append(attrs, t.stages...)
	attrs = append(attrs,
		"entry_lookups", t.Entries.Load(),
		"blob_reads", t.Blobs.Load(),
		"move_candidates", t.Candidates.Load(),
		"index_steps", t.IndexSteps.Load(),
		"tree_steps", t.TreeSteps.Load(),
	)
	if walked != nil {
		attrs = append(attrs,
			"lines", len(walked.Owners),
			"walked_commits", walked.Walked,
			"steps", walked.Steps,
			"snapshot_hit", walked.SnapshotHit,
			"truncated", walked.Truncated,
		)
	}
	return attrs
}

// Result holds the computed per-line owners and walk statistics.
type Result struct {
	Owners      []string
	Truncated   bool
	Walked      int
	Steps       int
	SnapshotHit bool
	// Guessed is true when the walk ran out of listed history before creation
	// or a snapshot (and without hitting MaxCommits), so no snapshot was stored.
	Guessed bool
	// Commits holds the commits the walk read, by id.
	Commits map[string]*corev1.Commit
}

// Step is one commit of the walk: the file's version right after the commit,
// and right before it (nil when the commit created it).
type Step struct {
	Commit  *corev1.Commit
	Path    string
	Version Version
	Parent  *Version
}

// Version holds one file version's content hash and split lines.
type Version struct {
	Hash  string
	Lines []string
}

// Entry finds the file at p in commitID; found is false when there is none.
func (e *Engine) Entry(ctx context.Context, commitID, p string) (*storage.FileEntry, bool, error) {
	if t := TraceFrom(ctx); t != nil {
		t.Entries.Add(1)
	}
	entry, err := e.Repository.GetFile(ctx, commitID, p)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if entry.Size > MaxBytes {
		return nil, false, fmt.Errorf("%w: %s is larger than %d bytes, too large to blame", ErrTooLarge, p, MaxBytes)
	}
	return entry, true, nil
}

// Lines reads a file version's lines, refusing binary or oversized files.
func (e *Engine) Lines(ctx context.Context, p, contentHash string, size int64) ([]string, error) {
	if size > MaxBytes {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes, too large to blame", ErrTooLarge, p, MaxBytes)
	}
	if t := TraceFrom(ctx); t != nil {
		t.Blobs.Add(1)
	}
	rc, err := e.Objects.Get(ctx, objectstore.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, fmt.Errorf("%w: %s is a binary file", ErrBinary, p)
	}
	return SplitLines(data), nil
}

// LinesByHash reads a file version's lines by content hash alone, capping read
// size at MaxBytes.
func (e *Engine) LinesByHash(ctx context.Context, contentHash string) ([]string, error) {
	if t := TraceFrom(ctx); t != nil {
		t.Blobs.Add(1)
	}
	rc, err := e.Objects.Get(ctx, objectstore.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(rc, MaxBytes+1)); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: a version of the file is larger than %d bytes, too large to blame", ErrTooLarge, MaxBytes)
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, fmt.Errorf("%w: a version of the file is binary", ErrBinary)
	}
	return SplitLines(data), nil
}

// VersionAt reads the file at p in commitID; found is false when absent.
func (e *Engine) VersionAt(ctx context.Context, commitID, p string) (Version, bool, error) {
	entry, found, err := e.Entry(ctx, commitID, p)
	if err != nil || !found {
		return Version{}, found, err
	}
	lines, err := e.Lines(ctx, p, entry.ContentHash, entry.Size)
	if err != nil {
		return Version{}, false, err
	}
	return Version{Hash: entry.ContentHash, Lines: lines}, true, nil
}

// MovedFrom finds where a file was before a commit that moved it: among the
// paths the commit changed, one that had a file in the parent and none after,
// choosing the one sharing the most lines with the file.
func (e *Engine) MovedFrom(ctx context.Context, commit *corev1.Commit, movedTo string, moved Version, canReadPath func(string) bool) (string, bool, error) {
	if len(commit.ParentIds) == 0 {
		return "", false, nil
	}
	have := map[string]int{}
	for _, line := range moved.Lines {
		have[line]++
	}
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
	if t := TraceFrom(ctx); t != nil {
		t.Candidates.Add(int64(len(candidates)))
	}
	shared := make([]int, len(candidates))
	for i := range shared {
		shared[i] = -1
	}
	var group errgroup.Group
	group.SetLimit(MoveConcurrency)
	for i, candidate := range candidates {
		group.Go(func() error {
			parentEntry, found, err := e.Entry(ctx, commit.ParentIds[0], candidate)
			if err != nil || !found {
				return nil
			}
			if _, stillThere, err := e.Entry(ctx, commit.Id, candidate); err != nil || stillThere {
				return nil
			}
			lines, err := e.Lines(ctx, candidate, parentEntry.ContentHash, parentEntry.Size)
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
	best, bestShared := "", -1
	for i, candidate := range candidates {
		if shared[i] > bestShared {
			best, bestShared = candidate, shared[i]
		}
	}
	return best, redacted && best == "", nil
}

// SnapshotReadable reports whether every commit credited by snapshot touches
// at least one path readable to the caller.
func (e *Engine) SnapshotReadable(ctx context.Context, snapshot *storage.BlameSnapshot, known map[string]*corev1.Commit, canReadPath func(string) bool) bool {
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
		read, err := e.Repository.GetCommits(ctx, missing)
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

// ListCommitPage reads one page of commits matching filters.
func (e *Engine) ListCommitPage(ctx context.Context, refName string, filters CommitFilters, limit int, token string) (*storage.CommitListPage, error) {
	switch {
	case filters.Filtered && len(filters.Prefixes) == 0 && len(filters.EntityRefs) == 0:
		return &storage.CommitListPage{}, nil
	case len(filters.EntityRefs) > 0 && filters.IncludePrefixesWithEntities:
		return e.Repository.ListCommitPageByEntityRefsOrPathPrefixes(ctx, refName, filters.EntityRefs, filters.Prefixes, limit, token)
	case len(filters.EntityRefs) > 0:
		return e.Repository.ListCommitPageByEntityRefs(ctx, refName, filters.EntityRefs, limit, token)
	case len(filters.Prefixes) == 0:
		return e.Repository.ListCommitPage(ctx, refName, limit, token)
	default:
		return e.Repository.ListCommitPageByPathPrefixes(ctx, refName, filters.Prefixes, limit, token)
	}
}

// HistoryEntityRefsForPathPrefixes resolves history entity refs for move-following.
func (e *Engine) HistoryEntityRefsForPathPrefixes(ctx context.Context, refName, pathFilter string, prefixes []string) ([]storage.HistoryEntityRef, bool, error) {
	entities, err := e.Repository.CurrentPathEntitiesByPrefixes(ctx, refName, prefixes)
	if err != nil {
		return nil, false, err
	}
	exactEntities, err := e.Repository.CurrentPathEntitiesByPaths(ctx, refName, []string{pathFilter})
	if err != nil {
		return nil, false, err
	}
	ancestorEntities, err := e.Repository.CurrentPathEntitiesByPaths(ctx, refName, ancestorPaths(pathFilter))
	if err != nil {
		return nil, false, err
	}
	entities = append(entities, ancestorEntities...)
	seen := map[string]struct{}{}
	out := make([]storage.HistoryEntityRef, 0, len(entities))
	for _, entity := range entities {
		if entity.AccountID == "" || entity.EntityID == "" {
			continue
		}
		key := entity.AccountID + "\x00" + entity.EntityID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, storage.HistoryEntityRef{AccountID: entity.AccountID, EntityID: entity.EntityID})
	}
	includePrefixes := true
	for _, entity := range exactEntities {
		if entity.Path == pathFilter && entity.Kind == "file" {
			includePrefixes = false
			break
		}
	}
	return out, includePrefixes, nil
}

func ancestorPaths(p string) []string {
	p = strings.TrimRight(p, "/")
	if p == "" || p == "/" {
		return nil
	}
	segments := strings.Split(strings.Trim(p, "/"), "/")
	if len(segments) <= 1 {
		return nil
	}
	out := make([]string, 0, len(segments)-1)
	for i := 1; i < len(segments); i++ {
		out = append(out, "/"+strings.Join(segments[:i], "/"))
	}
	return out
}

// SplitLines splits file data into newline-terminated line strings.
func SplitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
