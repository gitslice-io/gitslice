package gitcompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// A slice's Git projection is an append-only history: one Git commit per
// native commit on refs/global/main that changes the slice's projected tree,
// each parented on the previous one. Every object is a pure function of the
// slice definition and the native history (no wall-clock time, randomness or
// map order), so independent server instances with empty caches compute the
// same commit ids, and published ids (for example Go pseudo-versions) stay
// valid. The state file only lets an instance extend its own cache instead of
// replaying the whole history on every request.

// updateHistory brings the projected repository at repoPath up to date with
// the native ref and returns the resulting state. Callers hold the repo lock.
func (p *Projector) updateHistory(ctx context.Context, repoPath, account, sliceSlug string, slice *corev1.Slice) (*projectionState, error) {
	prefixes, err := projectionPrefixes(slice)
	if err != nil {
		return nil, err
	}
	state := loadProjectionState(repoPath)
	if !p.stateUsable(ctx, repoPath, state, slice) {
		state = newProjectionState(account, sliceSlug, slice)
		if err := resetProjectedRepo(ctx, repoPath); err != nil {
			return nil, err
		}
	}
	chain, err := p.repository.ListCommitChain(ctx, storage.DefaultTargetRef, state.NativeHeadID, prefixes)
	if err != nil {
		return nil, err
	}
	if state.NativeHeadID != "" && !chain.FoundStop {
		// The recorded native head is no longer on the ref's history, so the
		// cache cannot be extended. Rebuild; determinism makes this safe.
		state = newProjectionState(account, sliceSlug, slice)
		if err := resetProjectedRepo(ctx, repoPath); err != nil {
			return nil, err
		}
		if chain, err = p.repository.ListCommitChain(ctx, storage.DefaultTargetRef, "", prefixes); err != nil {
			return nil, err
		}
	}
	changed := false
	if chain.HeadCommitID != state.NativeHeadID {
		if err := p.appendHistory(ctx, repoPath, state, prefixes, chain.Commits); err != nil {
			return nil, err
		}
		state.NativeHeadID = chain.HeadCommitID
		changed = true
	}
	tagsChanged, err := p.syncTags(ctx, repoPath, state, slice, prefixes)
	if err != nil {
		return nil, err
	}
	if changed || tagsChanged {
		if err := writeProjectionState(repoPath, state); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func projectedIndex(state *projectionState, gitCommit string) (int, bool) {
	for i, commit := range state.Commits {
		if commit.Git == gitCommit {
			return i, true
		}
	}
	return 0, false
}

// projectedCommitAt returns the projected commit for the newest qualifying
// native commit at or before nativeID. A tag on a commit newer than the
// processed head is deferred: commits between the head and the tag may still
// qualify, so mapping it now could point it at the wrong commit.
func (p *Projector) projectedCommitAt(ctx context.Context, state *projectionState, history map[string]string, nativeID string) (string, bool, error) {
	ancestry, err := p.repository.CommitAncestry(ctx, nativeID, 0)
	if errors.Is(err, storage.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for i, id := range ancestry {
		if id == state.NativeHeadID && i > 0 {
			return "", false, nil
		}
		if git := history[id]; git != "" {
			return git, true, nil
		}
	}
	return "", false, nil
}

// stateUsable reports whether a loaded state matches this slice definition and
// the repository on disk. A crash between fast-import and the state write
// leaves the branch ahead of the state; that, like any mismatch, forces a
// rebuild rather than guessing.
func (p *Projector) stateUsable(ctx context.Context, repoPath string, state *projectionState, slice *corev1.Slice) bool {
	if state == nil || state.Version != projectionVersion || state.SliceID != slice.Id || state.DefinitionHash != slice.DefinitionHash || state.Files == nil {
		return false
	}
	head, err := gitOutput(ctx, repoPath, nil, "rev-parse", "--verify", "-q", projectedBranch)
	head = strings.TrimSpace(head)
	if err != nil {
		head = ""
	}
	return head == state.GitHead
}

func (p *Projector) appendHistory(ctx context.Context, repoPath string, state *projectionState, prefixes []string, commits []*corev1.Commit) error {
	if len(commits) == 0 {
		return nil
	}
	started := time.Now()
	targets := make([][]string, len(commits))
	for i, commit := range commits {
		targets[i] = projectionTargets(commit.ChangedPaths, prefixes)
	}
	listings, err := p.listCommitTargets(ctx, commits, targets)
	if err != nil {
		return err
	}
	listed := time.Now()
	var blobCount, blobBytes int
	var fetchTime, importTime time.Duration
	defer func() {
		slog.Info("git projection updated",
			"repo", filepath.Base(filepath.Dir(repoPath))+"/"+filepath.Base(repoPath),
			"native_commits", len(commits),
			"list_ms", listed.Sub(started).Milliseconds(),
			"blobs", blobCount,
			"blob_bytes", blobBytes,
			"fetch_ms", fetchTime.Milliseconds(),
			"import_ms", importTime.Milliseconds(),
			"total_ms", time.Since(started).Milliseconds(),
		)
	}()

	// Compute every commit's change set against a working copy of the tree, so
	// a failed write leaves the saved state untouched.
	files := make(map[string]projectedFile, len(state.Files))
	for path, file := range state.Files {
		files[path] = file
	}
	var pending []pendingCommit
	for i, commit := range commits {
		changes := applyListing(files, targets[i], listings[i])
		if len(changes) == 0 {
			continue
		}
		pending = append(pending, pendingCommit{native: commit, changes: changes})
	}
	if len(pending) == 0 {
		return nil
	}
	nativeIDs := make([]string, 0, len(pending))
	for _, commit := range pending {
		nativeIDs = append(nativeIDs, commit.native.Id)
	}
	imports, err := p.repository.GitImportsForCommits(ctx, nativeIDs)
	if err != nil {
		return err
	}
	for i := range pending {
		if record, ok := imports[pending[i].native.Id]; ok {
			record := record
			pending[i].imported = &record
		}
	}

	subjects := make([]string, 0, len(pending))
	seen := map[string]struct{}{}
	for _, commit := range pending {
		if _, ok := seen[commit.native.Author]; ok || commit.native.Author == "" {
			continue
		}
		seen[commit.native.Author] = struct{}{}
		subjects = append(subjects, commit.native.Author)
	}
	sort.Strings(subjects)
	usernames := map[string]string{}
	if len(subjects) > 0 && p.auth != nil {
		if usernames, err = p.auth.UsernamesForSubjects(ctx, subjects); err != nil {
			return err
		}
	}

	known := map[string]string{}
	for _, file := range state.Files {
		if file.Blob != "" {
			known[file.ContentHash] = file.Blob
		}
	}
	var need []string
	seenNeed := map[string]struct{}{}
	for _, commit := range pending {
		for _, change := range commit.changes {
			if change.Delete || known[change.ContentHash] != "" {
				continue
			}
			if _, ok := seenNeed[change.ContentHash]; ok {
				continue
			}
			seenNeed[change.ContentHash] = struct{}{}
			need = append(need, change.ContentHash)
		}
	}
	sort.Strings(need)
	importStart := time.Now()
	if p.blobs != nil {
		stats, err := p.appendLazy(ctx, repoPath, state, files, pending, usernames, known, need)
		if err != nil {
			return err
		}
		importTime = time.Since(importStart)
		blobCount, blobBytes, fetchTime = stats.blobs, stats.bytes, stats.fetch
		return nil
	}
	packsBefore, _ := listPacks(repoPath)
	shas, blobs, stats, err := runFastImport(ctx, repoPath, state.GitHead, pending, need, p.readBlob, known, usernames)
	if err != nil {
		return err
	}
	importTime = time.Since(importStart)
	blobCount, blobBytes, fetchTime = stats.blobs, stats.bytes, stats.fetch
	if packsAfter, err := listPacks(repoPath); err == nil {
		state.HistoryPacks = append(state.HistoryPacks, newPackNames(packsBefore, packsAfter)...)
	}
	for hash, sha := range blobs {
		known[hash] = sha
	}
	written := make([]projectedCommit, 0, len(shas))
	for i, sha := range shas {
		written = append(written, projectedCommit{Native: pending[i].native.Id, Git: sha})
	}
	parent := shas[len(shas)-1]

	for path, file := range files {
		if sha := known[file.ContentHash]; sha != "" {
			file.Blob = sha
			files[path] = file
		}
	}
	state.Files = files
	state.Commits = append(state.Commits, written...)
	state.GitHead = parent
	return nil
}

// listCommitTargets reads, for every commit, the files under each target path
// at that commit. Commits are independent reads, so they run concurrently.
func (p *Projector) listCommitTargets(ctx context.Context, commits []*corev1.Commit, targets [][]string) ([]map[string][]storage.FileEntry, error) {
	listings := make([]map[string][]storage.FileEntry, len(commits))
	jobs := make(chan int)
	errs := make(chan error, len(commits))
	var wg sync.WaitGroup
	for w := 0; w < projectionListConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				listing := make(map[string][]storage.FileEntry, len(targets[i]))
				for _, target := range targets[i] {
					files, err := p.repository.ListFiles(ctx, commits[i].Id, target)
					if err != nil && !errors.Is(err, storage.ErrNotFound) {
						errs <- fmt.Errorf("list %s at %s: %w", target, commits[i].Id, err)
						return
					}
					listing[target] = files
				}
				listings[i] = listing
			}
		}()
	}
	for i := range commits {
		select {
		case jobs <- i:
		case err := <-errs:
			close(jobs)
			wg.Wait()
			return nil, err
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	return listings, nil
}

func (p *Projector) readBlob(ctx context.Context, contentHash string) ([]byte, error) {
	rc, err := p.objectStore.Get(ctx, filesystem.BlobKey(contentHash), 0, 0)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", contentHash, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", contentHash, err)
	}
	return data, nil
}
