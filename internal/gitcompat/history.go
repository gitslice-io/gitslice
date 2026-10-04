package gitcompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
)

// A slice's Git projection is an append-only history: one Git commit per
// native commit on refs/global/main that changes the slice's projected tree,
// each parented on the previous one. Every object is a pure function of the
// slice definition and the native history (no wall-clock time, randomness or
// map order), so independent server instances with empty caches compute the
// same commit ids, and published ids (for example Go pseudo-versions) stay
// valid. The state file only lets an instance extend its own cache instead of
// replaying the whole history on every request.

// projectionVersion identifies the projection algorithm and the state file
// layout. Bumping it makes every cached projection rebuild from scratch.
const projectionVersion = 4

const (
	projectionStateFile = "gitslice_projection.json"
	projectedBranch     = "refs/heads/main"
	emptyGitTree        = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	noreplyDomain       = "users.noreply.gitslice.io"
	commitTrailer       = "Gitslice-Commit"
	gitCommitTrailer    = "Git-Commit"

	// Listing and blob reads are latency-bound object-store round trips, so a
	// cold build runs many at once.
	projectionListConcurrency = 48
	projectionBlobConcurrency = 48
)

type projectionState struct {
	Version        int    `json:"version"`
	Account        string `json:"account"`
	Slice          string `json:"slice"`
	SliceID        string `json:"slice_id"`
	DefinitionHash string `json:"definition_hash"`
	// NativeHeadID is the head of refs/global/main the projection reflects.
	NativeHeadID string `json:"native_head_id"`
	// GitHead is the projected head commit, "" while the history is empty.
	GitHead string `json:"git_head"`
	// Commits pairs each projected commit with its native commit, oldest first.
	Commits []projectedCommit `json:"commits"`
	// Files is the projected tree at GitHead, keyed by Git path.
	Files map[string]projectedFile `json:"files"`
	// Tags caches each native tag's projected commit by tag name.
	Tags map[string]projectedCommit `json:"tags,omitempty"`
	// HistoryPacks names the packs holding the projected history (commits and
	// trees, plus any files written with them), in the order they were built.
	// A mirror copies exactly these; packs of hydrated file contents are
	// excluded because the native store already holds those files.
	HistoryPacks []string `json:"history_packs,omitempty"`
}

type projectedCommit struct {
	Native string `json:"native"`
	Git    string `json:"git"`
}

type projectedFile struct {
	Mode        string `json:"mode"`
	ContentHash string `json:"content_hash"`
	// Blob is the Git blob id, known once the content has been written.
	Blob string `json:"blob,omitempty"`
}

type fileChange struct {
	Path        string // Git path, no leading slash
	Delete      bool
	Mode        string
	ContentHash string
	Size        int64
}

type pendingCommit struct {
	native  *corev1.Commit
	changes []fileChange
	// imported is set when the native commit was published by a Git import.
	imported *storage.GitImportedCommitRecord
}

func newProjectionState(account, sliceSlug string, slice *corev1.Slice) *projectionState {
	return &projectionState{
		Version:        projectionVersion,
		Account:        account,
		Slice:          sliceSlug,
		SliceID:        slice.Id,
		DefinitionHash: slice.DefinitionHash,
		Files:          map[string]projectedFile{},
	}
}

func (s *projectionState) projection() *Projection {
	history := make(map[string]string, len(s.Commits))
	for _, commit := range s.Commits {
		history[commit.Git] = commit.Native
	}
	return &Projection{
		Account:        s.Account,
		Slice:          s.Slice,
		SliceID:        s.SliceID,
		DefinitionHash: s.DefinitionHash,
		NativeCommitID: s.NativeHeadID,
		GitCommitID:    s.GitHead,
		history:        history,
	}
}

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

// goSemverTag matches the version tags Go understands.
var goSemverTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// syncTags publishes the slice's native tags as lightweight tags on the
// projected commit at or before each tagged native commit. For a slice with a
// single included path, a semver tag is also published under that path (for
// example acme/payment/v1.2.0): that is how Go names versions of a module
// whose root is a repository subdirectory, which the canonical layout puts
// every slice's files in. It reports whether the cached mapping changed.
func (p *Projector) syncTags(ctx context.Context, repoPath string, state *projectionState, slice *corev1.Slice, prefixes []string) (bool, error) {
	if p.slices == nil {
		return false, nil
	}
	tags, err := p.slices.ListTags(ctx, slice.Id)
	if err != nil {
		return false, err
	}
	history := make(map[string]string, len(state.Commits))
	for _, commit := range state.Commits {
		history[commit.Native] = commit.Git
	}
	if state.Tags == nil {
		state.Tags = map[string]projectedCommit{}
	}
	changed := false
	resolved := map[string]projectedCommit{}
	for _, tag := range tags {
		if cached, ok := state.Tags[tag.Name]; ok && cached.Native == tag.CommitID {
			// The cached projected commit must still be part of this history.
			if _, known := projectedIndex(state, cached.Git); known {
				resolved[tag.Name] = cached
				continue
			}
		}
		target, ok, err := p.projectedCommitAt(ctx, state, history, tag.CommitID)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		resolved[tag.Name] = projectedCommit{Native: tag.CommitID, Git: target}
		changed = true
	}
	if len(resolved) != len(state.Tags) {
		changed = true
	}
	state.Tags = resolved

	want := map[string]string{}
	subdir := ""
	if len(prefixes) == 1 && prefixes[0] != "/" {
		subdir = strings.Trim(prefixes[0], "/")
	}
	for name, target := range resolved {
		want["refs/tags/"+name] = target.Git
		if subdir != "" && goSemverTag.MatchString(name) {
			want["refs/tags/"+subdir+"/"+name] = target.Git
		}
	}
	return changed, applyTagRefs(ctx, repoPath, want)
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

// applyTagRefs makes refs/tags/* in the projected repository exactly want.
func applyTagRefs(ctx context.Context, repoPath string, want map[string]string) error {
	raw, err := gitOutput(ctx, repoPath, nil, "for-each-ref", "--format=%(refname) %(objectname)", "refs/tags/")
	if err != nil {
		return err
	}
	have := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if ref, oid, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			have[ref] = oid
		}
	}
	var commands strings.Builder
	refs := make([]string, 0, len(want))
	for ref := range want {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if have[ref] != want[ref] {
			fmt.Fprintf(&commands, "update %s %s\n", ref, want[ref])
		}
	}
	stale := make([]string, 0)
	for ref := range have {
		if _, ok := want[ref]; !ok {
			stale = append(stale, ref)
		}
	}
	sort.Strings(stale)
	for _, ref := range stale {
		fmt.Fprintf(&commands, "delete %s\n", ref)
	}
	if commands.Len() == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "update-ref", "--stdin")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoPath)
	cmd.Stdin = strings.NewReader(commands.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref failed: %w\n%s", err, string(out))
	}
	return nil
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
	if p.lazy && p.blobs != nil {
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

type importStats struct {
	blobs int
	bytes int
	fetch time.Duration // wall time until the last blob arrived
}

// runFastImport writes commits on top of parent with one git fast-import run.
// The blobs in need are fetched concurrently and streamed into fast-import as
// they arrive, so network reads overlap object writing and only a few blobs are
// held in memory at once. Marks are local to the stream, so arrival order does
// not affect any object id. It returns the new commit ids in order and the ids
// of the blobs it wrote.
func runFastImport(ctx context.Context, repoPath, parent string, commits []pendingCommit, need []string, fetch func(context.Context, string) ([]byte, error), known map[string]string, usernames map[string]string) ([]string, map[string]string, importStats, error) {
	var stats importStats
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	marksFile, err := os.CreateTemp(repoPath, ".marks-*")
	if err != nil {
		return nil, nil, stats, err
	}
	marksPath := marksFile.Name()
	_ = marksFile.Close()
	defer os.Remove(marksPath)

	cmd := exec.CommandContext(ctx, "git", "fast-import", "--quiet", "--done", "--export-marks="+marksPath)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, stats, err
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nil, nil, stats, err
	}
	fail := func(err error) ([]string, map[string]string, importStats, error) {
		cancel()
		_ = stdin.Close()
		_ = cmd.Wait()
		if output.Len() > 0 {
			err = fmt.Errorf("%w\n%s", err, output.String())
		}
		return nil, nil, stats, err
	}

	type fetched struct {
		hash string
		data []byte
		err  error
	}
	results := make(chan fetched, projectionBlobConcurrency)
	jobs := make(chan string)
	var workers sync.WaitGroup
	for i := 0; i < projectionBlobConcurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for hash := range jobs {
				data, err := fetch(ctx, hash)
				select {
				case results <- fetched{hash: hash, data: data, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, hash := range need {
			select {
			case jobs <- hash:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	w := bufio.NewWriterSize(stdin, 1<<20)
	fetchStart := time.Now()
	w.WriteString("feature done\n")
	blobMarks := make(map[string]int, len(need))
	mark := 0
	for result := range results {
		if result.err != nil {
			return fail(result.err)
		}
		mark++
		blobMarks[result.hash] = mark
		stats.blobs++
		stats.bytes += len(result.data)
		fmt.Fprintf(w, "blob\nmark :%d\ndata %d\n", mark, len(result.data))
		w.Write(result.data)
		if _, err := w.WriteString("\n"); err != nil {
			return fail(fmt.Errorf("git fast-import: %w", err))
		}
	}
	stats.fetch = time.Since(fetchStart)
	if len(blobMarks) != len(need) {
		return fail(fmt.Errorf("projection fetched %d of %d blobs", len(blobMarks), len(need)))
	}

	commitMarks := make([]int, len(commits))
	for i, commit := range commits {
		mark++
		commitMarks[i] = mark
		name, email := projectedIdentity(commit.native.Author, usernames)
		when := projectedTime(commit.native.CreatedAt)
		authorName, authorEmail, authored := importedAuthor(commit.imported, name, email, when)
		message := projectedCommitMessage(commit.native, commit.imported)
		fmt.Fprintf(w, "commit %s\nmark :%d\n", projectedBranch, mark)
		fmt.Fprintf(w, "author %s <%s> %d +0000\n", authorName, authorEmail, authored)
		fmt.Fprintf(w, "committer %s <%s> %d +0000\n", name, email, when)
		fmt.Fprintf(w, "data %d\n%s", len(message), message)
		if i == 0 && parent != "" {
			fmt.Fprintf(w, "from %s\n", parent)
		}
		for _, change := range commit.changes {
			if change.Delete {
				fmt.Fprintf(w, "D %s\n", fastImportPath(change.Path))
				continue
			}
			ref := known[change.ContentHash]
			if m, ok := blobMarks[change.ContentHash]; ok {
				ref = ":" + strconv.Itoa(m)
			}
			if ref == "" {
				return fail(fmt.Errorf("projection is missing content %s for %s", change.ContentHash, change.Path))
			}
			fmt.Fprintf(w, "M %s %s %s\n", change.Mode, ref, fastImportPath(change.Path))
		}
		w.WriteString("\n")
	}
	w.WriteString("done\n")
	if err := w.Flush(); err != nil {
		return fail(fmt.Errorf("git fast-import: %w", err))
	}
	if err := stdin.Close(); err != nil {
		return fail(err)
	}
	if err := cmd.Wait(); err != nil {
		return nil, nil, stats, fmt.Errorf("git fast-import failed: %w\n%s", err, output.String())
	}
	marks, err := readFastImportMarks(marksPath)
	if err != nil {
		return nil, nil, stats, err
	}
	shas := make([]string, len(commitMarks))
	for i, m := range commitMarks {
		if shas[i] = marks[m]; shas[i] == "" {
			return nil, nil, stats, fmt.Errorf("git fast-import did not report commit mark :%d", m)
		}
	}
	blobs := make(map[string]string, len(blobMarks))
	for hash, m := range blobMarks {
		blobs[hash] = marks[m]
	}
	return shas, blobs, stats, nil
}

func readFastImportMarks(path string) (map[int]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	marks := map[int]string{}
	for _, line := range strings.Split(string(data), "\n") {
		markText, sha, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(markText, ":") {
			continue
		}
		mark, err := strconv.Atoi(strings.TrimPrefix(markText, ":"))
		if err != nil {
			return nil, fmt.Errorf("malformed fast-import mark %q", line)
		}
		marks[mark] = sha
	}
	return marks, nil
}

// projectionTargets returns the slice paths a commit may have changed: each
// changed path inside an included prefix, or the prefix itself when the
// changed path is an ancestor of it (an account root, a moved parent). Nested
// targets are dropped because their parent target already covers them.
func projectionTargets(changed, prefixes []string) []string {
	set := map[string]struct{}{}
	for _, p := range changed {
		p = "/" + strings.Trim(p, "/")
		for _, prefix := range prefixes {
			switch {
			case paths.Contains(prefix, p):
				set[p] = struct{}{}
			case paths.Contains(p, prefix):
				set[prefix] = struct{}{}
			}
		}
	}
	targets := make([]string, 0, len(set))
	for target := range set {
		covered := false
		for other := range set {
			if other != target && paths.Contains(other, target) {
				covered = true
				break
			}
		}
		if !covered {
			targets = append(targets, target)
		}
	}
	sort.Strings(targets)
	return targets
}

// applyListing replaces everything under each target with the listed files,
// updates files in place, and returns the resulting changes sorted by path.
func applyListing(files map[string]projectedFile, targets []string, listing map[string][]storage.FileEntry) []fileChange {
	next := map[string]*fileChange{}
	for _, target := range targets {
		gitTarget := strings.Trim(target, "/")
		for path := range files {
			if gitTarget == "" || path == gitTarget || strings.HasPrefix(path, gitTarget+"/") {
				next[path] = &fileChange{Path: path, Delete: true}
			}
		}
		for _, file := range listing[target] {
			path := strings.TrimPrefix(file.Path, "/")
			if !projectablePath(path) {
				continue
			}
			next[path] = &fileChange{Path: path, Mode: gitFileMode(file.Mode), ContentHash: file.ContentHash, Size: file.Size}
		}
	}
	changes := make([]fileChange, 0, len(next))
	for path, change := range next {
		old, existed := files[path]
		if change.Delete {
			if existed {
				delete(files, path)
				changes = append(changes, *change)
			}
			continue
		}
		if existed && old.Mode == change.Mode && old.ContentHash == change.ContentHash {
			continue
		}
		blob := ""
		if existed && old.ContentHash == change.ContentHash {
			blob = old.Blob
		}
		files[path] = projectedFile{Mode: change.Mode, ContentHash: change.ContentHash, Blob: blob}
		changes = append(changes, *change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// projectablePath rejects paths Git cannot store, such as anything inside a
// ".git" directory.
func projectablePath(path string) bool {
	if path == "" {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// gitFileMode maps a native file mode to a Git tree mode. Symlinks keep their
// type; their content is the link target, as in Git.
func gitFileMode(mode uint32) string {
	switch {
	case mode&0o170000 == 0o120000:
		return "120000"
	case mode&0o111 != 0:
		return "100755"
	default:
		return "100644"
	}
}

func projectedIdentity(subjectID string, usernames map[string]string) (string, string) {
	name := usernames[subjectID]
	if name == "" || strings.ContainsAny(name, "<>\n\r\x00") {
		return "Gitslice", "gitslice@" + noreplyDomain
	}
	return name, name + "@" + noreplyDomain
}

func projectedTime(createdAt string) int64 {
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return 0
	}
	return t.UTC().Unix()
}

// projectedCommitMessage is the commit message followed by a trailer naming
// the native commit, which lets clients and the GitHub exporter map a projected
// commit back to Gitslice. A commit published by a Git import keeps its
// original full message and gains a Git-Commit trailer naming the original
// commit.
func projectedCommitMessage(commit *corev1.Commit, imported *storage.GitImportedCommitRecord) string {
	source := commit.Message
	if imported != nil && strings.TrimSpace(imported.FullMessage) != "" {
		source = imported.FullMessage
	}
	message := strings.TrimRight(strings.ReplaceAll(source, "\r\n", "\n"), " \t\n")
	if strings.TrimSpace(message) == "" {
		message = "Gitslice commit " + shortNativeID(commit.Id)
	}
	trailers := ""
	if imported != nil && imported.GitCommitID != "" {
		trailers = gitCommitTrailer + ": " + imported.GitCommitID + "\n"
	}
	return message + "\n\n" + trailers + commitTrailer + ": " + commit.Id + "\n"
}

// importedAuthor returns the original Git author of an imported commit, or
// the native identity when there is no usable record. The committer stays the
// native identity, which records who published the import and when.
func importedAuthor(imported *storage.GitImportedCommitRecord, name, email string, when int64) (string, string, int64) {
	if imported == nil || imported.AuthorName == "" || strings.ContainsAny(imported.AuthorName+imported.AuthorEmail, "<>\n\r\x00") {
		return name, email, when
	}
	authored := when
	if imported.AuthoredAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, imported.AuthoredAt); err == nil {
			authored = t.UTC().Unix()
		}
	}
	return imported.AuthorName, imported.AuthorEmail, authored
}

func shortNativeID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// fastImportPath quotes a path the way git fast-import requires when it starts
// with a double quote or contains a line break or backslash.
func fastImportPath(path string) string {
	if !strings.ContainsAny(path, "\"\n\\") {
		return path
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func projectionPrefixes(slice *corev1.Slice) ([]string, error) {
	var prefixes []string
	for _, prefix := range slice.Definition.IncludedPaths {
		// Included paths may be account roots (one segment) for home slices,
		// so use CanonicalPrefix rather than Canonical.
		canonical, err := paths.CanonicalPrefix(prefix)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, canonical)
	}
	sort.Strings(prefixes)
	return prefixes, nil
}

func loadProjectionState(repoPath string) *projectionState {
	data, err := os.ReadFile(filepath.Join(repoPath, projectionStateFile))
	if err != nil {
		return nil
	}
	var state projectionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

func writeProjectionState(repoPath string, state *projectionState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(repoPath, ".state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(repoPath, projectionStateFile))
}

// ensureProjectedRepo creates the bare repository for a projection if needed.
func ensureProjectedRepo(ctx context.Context, repoPath string) error {
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return err
	}
	if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", repoPath); err != nil {
		return err
	}
	return configureProjectedRepo(ctx, repoPath)
}

// resetProjectedRepo drops the projected branch (objects stay until git gc)
// and reapplies the repository settings, which older caches may lack.
func resetProjectedRepo(ctx context.Context, repoPath string) error {
	if _, err := gitOutput(ctx, repoPath, nil, "rev-parse", "--verify", "-q", projectedBranch); err == nil {
		if err := runGit(ctx, repoPath, nil, "update-ref", "-d", projectedBranch); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(repoPath, projectionStateFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return configureProjectedRepo(ctx, repoPath)
}

func configureProjectedRepo(ctx context.Context, repoPath string) error {
	settings := [][]string{
		{"config", "http.receivepack", "false"},
		// Go fetches pseudo-versions by commit id.
		{"config", "uploadpack.allowReachableSHA1InWant", "true"},
		// The history packs are what a mirror copies; a repack would replace
		// them with packs it has not seen.
		{"config", "gc.auto", "0"},
		{"symbolic-ref", "HEAD", projectedBranch},
	}
	for _, args := range settings {
		if err := runGit(ctx, repoPath, nil, args...); err != nil {
			return err
		}
	}
	return nil
}

// newPackNames returns the names in after that are not in before.
func newPackNames(before, after []string) []string {
	had := make(map[string]bool, len(before))
	for _, name := range before {
		had[name] = true
	}
	var out []string
	for _, name := range after {
		if !had[name] {
			out = append(out, name)
		}
	}
	return out
}
