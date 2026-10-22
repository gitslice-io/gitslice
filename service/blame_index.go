package service

import (
	"bytes"
	"context"
	"io"
	"sync"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Planning a blame from the history index. A blame walk needs, for each commit
// that changed the file, the version before it. Reading it from the commit's
// tree costs a read per directory level from storage, 1-2 s for an old commit
// whose tree nodes are not cached, and a long history has many. The history
// index already records the content each commit left a file with, so most
// steps need only the content, one read by its hash.
//
// A step is planned from the index only when it is certain: the commit has one
// recorded change at the file's path, leaving the content the walk is tracking,
// and the version before it is the content the file had after the nearest
// older listed commit that changed it. Anything else (a commit the index has
// not reached, a directory move, a deletion, a copy) is read from the tree, as
// it always was.

// blameChanges is a page's recorded file changes, by commit and path.
type blameChanges map[string]map[string][]storage.FileChange

func newBlameChanges(list []storage.FileChange) blameChanges {
	out := blameChanges{}
	for _, change := range list {
		if out[change.CommitID] == nil {
			out[change.CommitID] = map[string][]storage.FileChange{}
		}
		out[change.CommitID][change.Path] = append(out[change.CommitID][change.Path], change)
	}
	return out
}

// blameIndexStep is a step planned from the index: the file was created, or
// the version before it was parentHash at parentPath.
type blameIndexStep struct {
	created    bool
	parentPath string
	parentHash string
}

// one is the single change recorded for path in a commit, if there is exactly
// one.
func (c blameChanges) one(commitID, path string) (storage.FileChange, bool) {
	rows := c[commitID][path]
	if len(rows) != 1 {
		return storage.FileChange{}, false
	}
	return rows[0], true
}

// step plans commits[index], which left the file at path with content hash.
func (c blameChanges) step(commits []*corev1.Commit, index int, path, hash string) (blameIndexStep, bool) {
	change, ok := c.one(commits[index].Id, path)
	if !ok || change.ContentHash != hash {
		return blameIndexStep{}, false
	}
	prevPath := path
	switch change.ChangeKind {
	case "added":
		if change.OldPath != "" {
			return blameIndexStep{}, false
		}
		return blameIndexStep{created: true}, true
	case "modified":
		if change.OldPath != "" {
			return blameIndexStep{}, false
		}
	case "moved":
		if change.OldPath == "" {
			return blameIndexStep{}, false
		}
		prevPath = change.OldPath
	default:
		return blameIndexStep{}, false
	}
	for j := index + 1; j < len(commits); j++ {
		if len(c[commits[j].Id][prevPath]) == 0 {
			// Listed, yet with no recorded change to the file: the index
			// has not reached it, or it moved a directory above the file.
			// Either way the version before is not certain.
			return blameIndexStep{}, false
		}
		prev, ok := c.one(commits[j].Id, prevPath)
		if !ok || prev.ContentHash == "" || (prev.ChangeKind != "added" && prev.ChangeKind != "modified" && prev.ChangeKind != "moved") {
			return blameIndexStep{}, false
		}
		return blameIndexStep{parentPath: prevPath, parentHash: prev.ContentHash}, true
	}
	return blameIndexStep{}, false
}

// chain plans consecutive steps from commits[index], for a file at path with
// content hash, as far as the index is certain, so their contents can be read
// ahead. It stops where the walk would (a stored blame of the version it is
// tracking) and where the index cannot answer.
func (c blameChanges) chain(commits []*corev1.Commit, index int, path, hash string, snapshots map[string]map[string]*storage.BlameSnapshot, limit int) []blameIndexStep {
	var out []blameIndexStep
	for i := index; i < len(commits) && len(out) < limit; i++ {
		if snapshot := snapshots[commits[i].Id][path]; snapshot != nil && snapshot.ContentHash == hash {
			break
		}
		if len(commits[i].ParentIds) == 0 {
			out = append(out, blameIndexStep{created: true})
			break
		}
		step, ok := c.step(commits, i, path, hash)
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

// blameBlobs reads file contents by hash, several at a time, ahead of the walk.
type blameBlobs struct {
	ctx     context.Context
	cancel  context.CancelFunc
	s       *RepositoryService
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
	blameBlobConcurrency = 8
	// blameBlobReadAhead bounds the contents read ahead, by count and by the
	// memory they take (a long file with a long history is many large reads).
	blameBlobReadAhead      = 64
	blameBlobReadAheadBytes = 32 << 20
)

func newBlameBlobs(ctx context.Context, s *RepositoryService) *blameBlobs {
	ctx, cancel := context.WithCancel(ctx)
	return &blameBlobs{ctx: ctx, cancel: cancel, s: s, reads: map[string]*blobRead{}, sem: make(chan struct{}, blameBlobConcurrency)}
}

// prefetch starts reading the parent contents of a planned chain, as many as
// fit the read-ahead budget for a file of about size bytes.
func (b *blameBlobs) prefetch(steps []blameIndexStep, size int64) {
	budget := blameBlobReadAhead
	if size > 0 {
		budget = min(budget, int(max(int64(2), blameBlobReadAheadBytes/size)))
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

func (b *blameBlobs) start(hash string) *blobRead {
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
		read.lines, read.err = b.s.blameLinesByHash(b.ctx, hash)
	}()
	return read
}

// lines returns the lines of the content with hash, reading it if it was not
// started, and forgets it (the walk uses each version once).
func (b *blameBlobs) lines(hash string) ([]string, error) {
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

func (b *blameBlobs) stop() { b.cancel() }

// blameLinesByHash reads a file version's lines by content hash alone, as
// blameLines does when the size is known.
func (s *RepositoryService) blameLinesByHash(ctx context.Context, contentHash string) ([]string, error) {
	if t := blameTraceOf(ctx); t != nil {
		t.blobs.Add(1)
	}
	rc, err := s.ObjectStore.Get(ctx, filesystem.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, grpcError(err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(rc, blameMaxBytes+1)); err != nil {
		return nil, grpcError(err)
	}
	data := buf.Bytes()
	if len(data) > blameMaxBytes {
		return nil, status.Errorf(codes.FailedPrecondition, "a version of the file is larger than %d bytes, too large to blame", blameMaxBytes)
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, status.Error(codes.InvalidArgument, "a version of the file is binary")
	}
	return splitBlameLines(data), nil
}
