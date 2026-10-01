package gitcompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
const projectionVersion = 2

const (
	projectionStateFile = "gitslice_projection.json"
	projectedBranch     = "refs/heads/main"
	emptyGitTree        = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	noreplyDomain       = "users.noreply.gitslice.io"
	commitTrailer       = "Gitslice-Commit"

	projectionListConcurrency = 8
	projectionBlobConcurrency = 16
	// projectionBatchBytes bounds the blob bytes held in memory for one
	// fast-import run; longer histories are written in several runs.
	projectionBatchBytes = 64 << 20
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
	targets := make([][]string, len(commits))
	for i, commit := range commits {
		targets[i] = projectionTargets(commit.ChangedPaths, prefixes)
	}
	listings, err := p.listCommitTargets(ctx, commits, targets)
	if err != nil {
		return err
	}

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
	parent := state.GitHead
	var written []projectedCommit
	for start := 0; start < len(pending); {
		end, need := planProjectionBatch(pending, start, known)
		contents, err := p.fetchBlobs(ctx, need)
		if err != nil {
			return err
		}
		shas, blobs, err := runFastImport(ctx, repoPath, parent, pending[start:end], contents, known, usernames)
		if err != nil {
			return err
		}
		for hash, sha := range blobs {
			known[hash] = sha
		}
		for i, sha := range shas {
			written = append(written, projectedCommit{Native: pending[start+i].native.Id, Git: sha})
		}
		parent = shas[len(shas)-1]
		start = end
	}

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

// planProjectionBatch picks pending[start:end] so that the bytes of blobs not
// yet in the repository stay under projectionBatchBytes (always at least one
// commit), and returns the content hashes the batch must fetch.
func planProjectionBatch(pending []pendingCommit, start int, known map[string]string) (int, map[string]int64) {
	need := map[string]int64{}
	var total int64
	end := start
	for end < len(pending) {
		added := map[string]int64{}
		var addedSize int64
		for _, change := range pending[end].changes {
			if change.Delete || known[change.ContentHash] != "" {
				continue
			}
			if _, ok := need[change.ContentHash]; ok {
				continue
			}
			if _, ok := added[change.ContentHash]; ok {
				continue
			}
			added[change.ContentHash] = change.Size
			addedSize += change.Size
		}
		if end > start && total+addedSize > projectionBatchBytes {
			break
		}
		for hash, size := range added {
			need[hash] = size
		}
		total += addedSize
		end++
	}
	return end, need
}

func (p *Projector) fetchBlobs(ctx context.Context, need map[string]int64) (map[string][]byte, error) {
	hashes := make([]string, 0, len(need))
	for hash := range need {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	contents := make(map[string][]byte, len(hashes))
	var mu sync.Mutex
	jobs := make(chan string)
	errs := make(chan error, len(hashes))
	var wg sync.WaitGroup
	for w := 0; w < projectionBlobConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for hash := range jobs {
				data, err := p.readBlob(ctx, hash)
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				contents[hash] = data
				mu.Unlock()
			}
		}()
	}
	for _, hash := range hashes {
		select {
		case jobs <- hash:
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
	return contents, nil
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

// runFastImport writes one batch of commits on top of parent and returns the
// new commit ids in order plus the ids of the blobs it wrote.
func runFastImport(ctx context.Context, repoPath, parent string, commits []pendingCommit, contents map[string][]byte, known map[string]string, usernames map[string]string) ([]string, map[string]string, error) {
	var stream bytes.Buffer
	stream.WriteString("feature done\n")
	blobMarks := map[string]int{}
	hashes := make([]string, 0, len(contents))
	for hash := range contents {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	mark := 0
	for _, hash := range hashes {
		mark++
		blobMarks[hash] = mark
		data := contents[hash]
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n", mark, len(data))
		stream.Write(data)
		stream.WriteString("\n")
	}
	commitMarks := make([]int, len(commits))
	for i, commit := range commits {
		mark++
		commitMarks[i] = mark
		name, email := projectedIdentity(commit.native.Author, usernames)
		when := projectedTime(commit.native.CreatedAt)
		message := projectedCommitMessage(commit.native)
		fmt.Fprintf(&stream, "commit %s\nmark :%d\n", projectedBranch, mark)
		fmt.Fprintf(&stream, "author %s <%s> %d +0000\n", name, email, when)
		fmt.Fprintf(&stream, "committer %s <%s> %d +0000\n", name, email, when)
		fmt.Fprintf(&stream, "data %d\n%s", len(message), message)
		if i == 0 && parent != "" {
			fmt.Fprintf(&stream, "from %s\n", parent)
		}
		for _, change := range commit.changes {
			if change.Delete {
				fmt.Fprintf(&stream, "D %s\n", fastImportPath(change.Path))
				continue
			}
			ref := known[change.ContentHash]
			if m, ok := blobMarks[change.ContentHash]; ok {
				ref = ":" + strconv.Itoa(m)
			}
			if ref == "" {
				return nil, nil, fmt.Errorf("projection is missing content %s for %s", change.ContentHash, change.Path)
			}
			fmt.Fprintf(&stream, "M %s %s %s\n", change.Mode, ref, fastImportPath(change.Path))
		}
		stream.WriteString("\n")
	}
	stream.WriteString("done\n")

	marksFile, err := os.CreateTemp(repoPath, ".marks-*")
	if err != nil {
		return nil, nil, err
	}
	marksPath := marksFile.Name()
	_ = marksFile.Close()
	defer os.Remove(marksPath)

	cmd := exec.CommandContext(ctx, "git", "fast-import", "--quiet", "--done", "--export-marks="+marksPath)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoPath)
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("git fast-import failed: %w\n%s", err, string(out))
	}
	marks, err := readFastImportMarks(marksPath)
	if err != nil {
		return nil, nil, err
	}
	shas := make([]string, len(commitMarks))
	for i, m := range commitMarks {
		if shas[i] = marks[m]; shas[i] == "" {
			return nil, nil, fmt.Errorf("git fast-import did not report commit mark :%d", m)
		}
	}
	blobs := make(map[string]string, len(blobMarks))
	for hash, m := range blobMarks {
		blobs[hash] = marks[m]
	}
	return shas, blobs, nil
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

func gitFileMode(mode uint32) string {
	if mode&0o111 != 0 {
		return "100755"
	}
	return "100644"
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

// projectedCommitMessage is the native message followed by a trailer naming
// the native commit, which lets clients and the GitHub exporter map a projected
// commit back to Gitslice.
func projectedCommitMessage(commit *corev1.Commit) string {
	message := strings.TrimRight(strings.ReplaceAll(commit.Message, "\r\n", "\n"), " \t\n")
	if strings.TrimSpace(message) == "" {
		message = "Gitslice commit " + shortNativeID(commit.Id)
	}
	return message + "\n\n" + commitTrailer + ": " + commit.Id + "\n"
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
		{"symbolic-ref", "HEAD", projectedBranch},
	}
	for _, args := range settings {
		if err := runGit(ctx, repoPath, nil, args...); err != nil {
			return err
		}
	}
	return nil
}
