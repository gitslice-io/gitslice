package gitcompat

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/objectstore"
)

// lazyChunkCommits is how many commits one pack holds. After each chunk the
// projection is complete up to that commit and its state is saved, so a build
// that is interrupted resumes from the last chunk instead of starting over.
const lazyChunkCommits = 1000

// appendLazy adds pending commits to the projection without reading file
// contents. A commit's id depends on its tree, a tree's on its files' Git blob
// ids, and those ids were recorded when the files were uploaded, so trees and
// commits are computed from metadata and written as one pack per chunk. The
// files themselves are missing from the repository until a client fetch needs
// them (hydrate).
//
// A file without a recorded id (uploaded before ids existed, and not yet
// backfilled) is read and written into the pack, as the eager path would.
func (p *Projector) appendLazy(ctx context.Context, repoPath string, state *projectionState, files map[string]projectedFile, pending []pendingCommit, usernames map[string]string, known map[string]string, need []string) (stats importStats, err error) {
	started := time.Now()
	// Git ids from the database for everything we do not already know.
	ids, err := p.blobs.GitBlobIDs(ctx, need)
	if err != nil {
		return stats, err
	}
	var read []string
	for _, hash := range need {
		if id := ids[hash]; id != "" {
			known[hash] = id
		} else {
			read = append(read, hash)
		}
	}
	sizes := map[string]int64{}
	for _, commit := range pending {
		for _, change := range commit.changes {
			sizes[change.ContentHash] = change.Size
		}
	}

	// The tree as of the last saved state; each commit is applied on top.
	builder, err := newTreeBuilder(state.Files)
	if err != nil {
		return stats, err
	}
	parent := state.GitHead
	saved := make(map[string]projectedFile, len(state.Files))
	for path, file := range state.Files {
		saved[path] = file
	}

	for start := 0; start < len(pending); start += lazyChunkCommits {
		end := min(start+lazyChunkCommits, len(pending))
		// Files this chunk introduces without an id are read now, in parallel,
		// each worker writing its own pack.
		filePacks, err := p.readUnrecorded(ctx, repoPath, chunkHashes(pending[start:end], read, known), sizes, known, &stats)
		if err != nil {
			return stats, err
		}
		pack, err := newPackBuilder(repoPath)
		if err != nil {
			return stats, err
		}
		var shas []string
		for _, commit := range pending[start:end] {
			for _, change := range commit.changes {
				blob := ""
				if !change.Delete {
					if blob = known[change.ContentHash]; blob == "" {
						pack.discard()
						return stats, fmt.Errorf("projection has no git id for %s (%s)", change.Path, change.ContentHash)
					}
				}
				builder.apply(change, blob)
			}
			tree := builder.root()
			for _, o := range builder.takeObjects() {
				if err := pack.add(o.kind, o.data); err != nil {
					pack.discard()
					return stats, err
				}
			}
			name, email := projectedIdentity(commit.native.Author, usernames)
			when := projectedTime(commit.native.CreatedAt)
			authorName, authorEmail, authored := importedAuthor(commit.imported, name, email, when)
			data := encodeCommit(tree, parent, authorName, authorEmail, authored, name, email, when, projectedCommitMessage(commit.native, commit.imported))
			parent = gitObjectID("commit", data)
			shas = append(shas, parent)
			if err := pack.add(objCommit, data); err != nil {
				pack.discard()
				return stats, err
			}
		}
		packName, err := pack.finish(ctx, repoPath)
		if err != nil {
			return stats, err
		}
		if err := updateProjectedBranch(ctx, repoPath, parent, state.GitHead); err != nil {
			return stats, err
		}
		// Checkpoint: the chunk is in the repository and recorded.
		for i, commit := range pending[start:end] {
			state.Commits = append(state.Commits, projectedCommit{Native: commit.native.Id, Git: shas[i]})
			for _, change := range commit.changes {
				if change.Delete {
					delete(saved, change.Path)
					continue
				}
				saved[change.Path] = projectedFile{Mode: change.Mode, ContentHash: change.ContentHash, Blob: known[change.ContentHash]}
			}
		}
		state.Files = copyFiles(saved)
		state.GitHead = parent
		state.NativeHeadID = pending[end-1].native.Id
		state.HistoryPacks = append(state.HistoryPacks, filePacks...)
		if packName != "" {
			state.HistoryPacks = append(state.HistoryPacks, packName)
		}
		if err := writeProjectionState(repoPath, state); err != nil {
			return stats, err
		}
	}
	if err := compactHistoryPacks(ctx, repoPath, state); err != nil {
		// The packs still work; merging is only housekeeping.
		slog.Warn("could not merge history packs", "repo", filepath.Base(filepath.Dir(repoPath))+"/"+filepath.Base(repoPath), "error", err)
	}
	stats.fetch = time.Since(started)
	for path, file := range files {
		if sha := known[file.ContentHash]; sha != "" {
			file.Blob = sha
			files[path] = file
		}
	}
	slog.Info("git projection built lazily", "repo", filepath.Base(filepath.Dir(repoPath))+"/"+filepath.Base(repoPath),
		"commits", len(pending), "ids_from_db", len(ids), "files_read", stats.blobs, "total_ms", time.Since(started).Milliseconds())
	return stats, nil
}

// chunkHashes returns the contents without a recorded id that the commits in a
// chunk introduce and that no earlier chunk has written.
func chunkHashes(commits []pendingCommit, unrecorded []string, known map[string]string) []string {
	want := map[string]bool{}
	for _, h := range unrecorded {
		want[h] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, commit := range commits {
		for _, change := range commit.changes {
			h := change.ContentHash
			if change.Delete || !want[h] || known[h] != "" || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

type fetchedBlob struct {
	rc  io.ReadCloser
	err error
}

// prefetchWindow is how many files one worker has requested ahead.
const prefetchWindow = 8

// prefetch starts reading the files, at most prefetchWindow ahead of the
// consumer, and returns their readers in order, one channel per file. The
// consumer closes each reader it takes; stop releases the rest.
func (p *Projector) prefetch(ctx context.Context, hashes []string) (next func() fetchedBlob, stop func()) {
	queue := make(chan chan fetchedBlob, prefetchWindow)
	done := make(chan struct{})
	go func() {
		defer close(queue)
		for _, h := range hashes {
			ch := make(chan fetchedBlob, 1)
			select {
			case queue <- ch:
			case <-done:
				return
			}
			go func() {
				rc, err := p.objectStore.Get(ctx, objectstore.BlobKey(h), 0, 0)
				ch <- fetchedBlob{rc: rc, err: err}
			}()
		}
	}()
	next = func() fetchedBlob { return <-<-queue }
	stop = func() {
		close(done)
		for ch := range queue {
			if f := <-ch; f.rc != nil {
				_ = f.rc.Close()
			}
		}
	}
	return next, stop
}

// readUnrecorded reads the files that have no recorded Git id from the object
// store, a few at a time, each worker streaming into a pack of its own, and
// records the ids it computed in known and in the database, so the next build
// does not read them again. It returns the packs it wrote. One at a time, a cold
// build of a slice with no recorded ids spent its time waiting on the store.
func (p *Projector) readUnrecorded(ctx context.Context, repoPath string, hashes []string, sizes map[string]int64, known map[string]string, stats *importStats) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	workers := min(hydrateShards, len(hashes))
	var mu sync.Mutex
	var names []string
	computed := map[string]string{}
	err := forEachPart(ctx, workers, workers, func(w int) error {
		pack, err := newPackBuilder(repoPath)
		if err != nil {
			return err
		}
		var written int64
		local := map[string]string{}
		mine := make([]string, 0, len(hashes)/workers+1)
		for i := w; i < len(hashes); i += workers {
			mine = append(mine, hashes[i])
		}
		// The store answers each request after a delay, so the next few files
		// are requested while the current one is written.
		next, stop := p.prefetch(ctx, mine)
		defer stop()
		for _, hash := range mine {
			f := next()
			if f.err != nil {
				pack.discard()
				return fmt.Errorf("read blob %s: %w", hash, f.err)
			}
			id, err := pack.addBlob(sizes[hash], f.rc)
			_ = f.rc.Close()
			if err != nil {
				pack.discard()
				return fmt.Errorf("add blob %s: %w", hash, err)
			}
			local[hash] = id
			written += sizes[hash]
		}
		name, err := pack.finish(ctx, repoPath)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if name != "" {
			names = append(names, name)
		}
		for hash, id := range local {
			computed[hash] = id
		}
		stats.blobs += len(local)
		stats.bytes += int(written)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for hash, id := range computed {
		known[hash] = id
	}
	if err := p.blobs.SetGitBlobIDs(ctx, computed); err != nil {
		slog.Warn("could not record git blob ids", "error", err)
	}
	sort.Strings(names)
	return names, nil
}

func copyFiles(files map[string]projectedFile) map[string]projectedFile {
	out := make(map[string]projectedFile, len(files))
	for path, file := range files {
		out[path] = file
	}
	return out
}

// updateProjectedBranch moves the projected branch to head, requiring it to be
// at old (empty for a new branch).
func updateProjectedBranch(ctx context.Context, repoPath, head, old string) error {
	if old == "" {
		old = "0000000000000000000000000000000000000000"
	}
	return runGit(ctx, repoPath, []string{"GIT_DIR=" + repoPath}, "update-ref", projectedBranch, head, old)
}

// A landing adds a handful of objects, so a busy slice would collect one tiny
// pack per commit; thousands of packs slow every lookup and bloat the mirror's
// manifest. History packs are merged in tiers: when the newest packs under a
// size all together number compactFanout, they become one pack.
const compactFanout = 16

var compactTiers = []int64{1 << 20, 16 << 20, 256 << 20, 4 << 30}

// compactHistoryPacks merges runs of small history packs and updates the
// state's list. Only history packs are touched: hydrated file contents are in
// packs of their own.
func compactHistoryPacks(ctx context.Context, repoPath string, state *projectionState) error {
	for {
		merge := mergeableRun(repoPath, state.HistoryPacks)
		if len(merge) == 0 {
			return nil
		}
		name, err := mergePacks(ctx, repoPath, merge)
		if err != nil {
			return err
		}
		drop := map[string]bool{}
		for _, n := range merge {
			drop[n] = true
		}
		kept := make([]string, 0, len(state.HistoryPacks))
		for _, n := range state.HistoryPacks {
			if !drop[n] {
				kept = append(kept, n)
			}
		}
		state.HistoryPacks = append(kept, name)
		// The state names the new pack before the old ones go.
		if err := writeProjectionState(repoPath, state); err != nil {
			return err
		}
		for _, n := range merge {
			if n == name {
				continue
			}
			for _, ext := range []string{".pack", ".idx", ".rev", ".bitmap"} {
				_ = os.Remove(filepath.Join(repoPath, "objects", "pack", n+ext))
			}
		}
	}
}

// mergeableRun returns the newest packs, all under one tier's size, when there
// are at least compactFanout of them.
func mergeableRun(repoPath string, packs []string) []string {
	sizes := make([]int64, len(packs))
	for i, name := range packs {
		if info, err := os.Stat(filepath.Join(repoPath, "objects", "pack", name+".pack")); err == nil {
			sizes[i] = info.Size()
		} else {
			sizes[i] = 1 << 62 // missing: never merge it
		}
	}
	for _, tier := range compactTiers {
		start := len(packs)
		for start > 0 && sizes[start-1] < tier {
			start--
		}
		if len(packs)-start >= compactFanout {
			return append([]string(nil), packs[start:]...)
		}
	}
	return nil
}

// mergePacks writes one pack holding every object of the named packs.
func mergePacks(ctx context.Context, repoPath string, names []string) (string, error) {
	var list strings.Builder
	for _, n := range names {
		list.WriteString(n + ".pack\n")
	}
	out, err := gitOutputInput(ctx, repoPath, []string{"GIT_DIR=" + repoPath}, []byte(list.String()), "pack-objects", "--quiet", "--stdin-packs", filepath.Join(repoPath, "objects", "pack", "pack"))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("git pack-objects did not name the merged pack")
	}
	return "pack-" + fields[len(fields)-1], nil
}
