package gitcompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
)

// ObjectStore is the object store that holds file contents and, under
// git-mirror/, the mirror of each slice's projection.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

type Projector struct {
	auth        storage.AuthStore
	repository  storage.RepositoryStore
	slices      storage.SliceStore
	objectStore ObjectStore
	cacheRoot   string
	blobs       storage.BlobStore

	mu    sync.Mutex
	locks map[string]*refMutex

	// mirror keeps a copy of every slice's projection outside this instance.
	// published records the native head last handed to it.
	mirror    *PackMirror
	published map[string]string
	// complete records, per repository, a head at which every file's contents
	// are present, so a full clone need not search for missing ones again.
	complete map[string]string

	// Covers (cover.go).
	compactRequester CompactRequester
	serveLocks       map[string]*refRWMutex
	coverAsked       map[string]time.Time
	fullFetches      map[string]bool
}

const maxTrackedRepos = 4096

type refMutex struct {
	mu   sync.Mutex
	refs int
}

type refRWMutex struct {
	mu   sync.RWMutex
	refs int
}

type ProjectorStores struct {
	Auth       storage.AuthStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
	// Blobs supplies the Git blob ids the projection builds trees from. Without
	// it history is imported with git fast-import, reading every file.
	Blobs storage.BlobStore
}

// Projection describes a slice's projected repository after an update.
type Projection struct {
	Account        string
	Slice          string
	SliceID        string
	DefinitionHash string
	// NativeCommitID is the head of refs/global/main the projection reflects.
	NativeCommitID string
	// GitCommitID is the projected head, or "" when the slice has no history.
	GitCommitID string
	// history maps every projected commit to its native commit.
	history map[string]string
}

// NativeCommitFor returns the native commit a projected commit was built from.
func (p *Projection) NativeCommitFor(gitCommitID string) (string, bool) {
	native, ok := p.history[gitCommitID]
	return native, ok
}

func NewProjector(stores ProjectorStores, objectStore ObjectStore, cacheRoot string) (*Projector, error) {
	if cacheRoot == "" {
		return nil, fmt.Errorf("git cache root is required")
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return nil, err
	}
	return &Projector{
		auth:        stores.Auth,
		repository:  stores.Repository,
		slices:      stores.Slices,
		blobs:       stores.Blobs,
		objectStore: objectStore,
		cacheRoot:   filepath.Clean(cacheRoot),
		locks:       map[string]*refMutex{},
		mirror:      NewPackMirror(objectStore),
		published:   map[string]string{},
	}, nil
}

func (p *Projector) CacheRoot() string {
	return p.cacheRoot
}

// validGitSlug reports whether s is a safe account or slice identifier that
// cannot traverse paths or address hidden cache directories.
func validGitSlug(s string) bool {
	if len(s) == 0 || len(s) > 63 || strings.Contains(s, "..") {
		return false
	}
	isAlphaNum := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	if !isAlphaNum(s[0]) || !isAlphaNum(s[len(s)-1]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if isAlphaNum(b) || b == '-' || b == '_' || b == '.' {
			continue
		}
		return false
	}
	return true
}

func (p *Projector) projectedRepoPath(account, sliceSlug string) (string, error) {
	if !validGitSlug(account) || !validGitSlug(sliceSlug) {
		return "", storage.ErrNotFound
	}
	repoPath := filepath.Join(p.cacheRoot, account, sliceSlug+".git")
	return p.verifyRepoPath(repoPath)
}

func (p *Projector) verifyRepoPath(repoPath string) (string, error) {
	if repoPath == "" {
		return "", errors.New("empty repository path")
	}
	cleanRepo := filepath.Clean(repoPath)
	if p.cacheRoot == "" {
		return cleanRepo, nil
	}
	cleanRoot := filepath.Clean(p.cacheRoot)
	rel, err := filepath.Rel(cleanRoot, cleanRepo)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repository path %q escapes cache root", repoPath)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || strings.HasPrefix(parts[0], ".") || strings.HasPrefix(parts[1], ".") || !strings.HasSuffix(parts[1], ".git") {
		return "", fmt.Errorf("invalid projected repository path %q", repoPath)
	}
	account := parts[0]
	sliceSlug := strings.TrimSuffix(parts[1], ".git")
	if !validGitSlug(account) || !validGitSlug(sliceSlug) {
		return "", fmt.Errorf("invalid projected repository slug in %q", repoPath)
	}
	return cleanRepo, nil
}

func (p *Projector) AuthorizeSlice(ctx context.Context, subjectID, account, sliceSlug string) error {
	if !validGitSlug(account) || !validGitSlug(sliceSlug) {
		return storage.ErrNotFound
	}
	slice, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return err
	}
	return authz.New(p.auth).Authorize(ctx, subjectID, slice, authz.ActionWrite)
}

func (p *Projector) EnsureProjectedRepo(ctx context.Context, subjectID, account, sliceSlug string) (string, *Projection, error) {
	if !validGitSlug(account) || !validGitSlug(sliceSlug) {
		return "", nil, storage.ErrNotFound
	}
	slice, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return "", nil, err
	}
	if err := authz.New(p.auth).Authorize(ctx, subjectID, slice, authz.ActionRead); err != nil {
		return "", nil, err
	}
	return p.ensureResolved(ctx, account, sliceSlug, slice)
}

// ensureProjected brings a slice's projection up to date for the server's own
// use (Compact), without a subject to authorize.
func (p *Projector) ensureProjected(ctx context.Context, account, sliceSlug string) (string, error) {
	if !validGitSlug(account) || !validGitSlug(sliceSlug) {
		return "", storage.ErrNotFound
	}
	slice, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return "", err
	}
	repoPath, _, err := p.ensureResolved(ctx, account, sliceSlug, slice)
	return repoPath, err
}

func (p *Projector) ensureResolved(ctx context.Context, account, sliceSlug string, slice *corev1.Slice) (string, *Projection, error) {
	repoPath, err := p.projectedRepoPath(account, sliceSlug)
	if err != nil {
		return "", nil, err
	}
	// Registered first, so it runs after the lock is released: a request that
	// changed the head stays open while the mirror catches up, because Cloud Run
	// only gives CPU to a container that is serving one.
	settle := false
	defer func() {
		if !settle {
			return
		}
		if err := p.mirror.Settle(ctx, account, sliceSlug); err != nil {
			// Publish again on the next request.
			p.setPublished(repoPath, "")
		}
	}()
	unlock := p.acquireLock(repoPath)
	defer unlock()

	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); errors.Is(err, os.ErrNotExist) {
		// A cold instance: restore what an earlier one mirrored before
		// replaying the whole history. The restore outlives this request.
		// Whatever this process remembers about the repository is about files it
		// no longer has.
		p.setComplete(repoPath, "")
		p.setPublished(repoPath, "")
		restored, err := p.mirror.Restore(context.WithoutCancel(ctx), account, sliceSlug, repoPath)
		switch {
		case err != nil:
			recordGitMirror("restore", "error")
			slog.Warn("git mirror restore failed; building from scratch", "repo", account+"/"+sliceSlug+".git", "error", err)
		case !restored:
			recordGitMirror("restore", "empty")
		default:
			recordGitMirror("restore", "ok")
			if state := loadProjectionState(repoPath); state != nil {
				p.setPublished(repoPath, state.NativeHeadID)
				// A restored cover holds every file up to its head.
				p.setComplete(repoPath, state.CompleteAt)
			}
		}
	}
	if err := ensureProjectedRepo(ctx, repoPath); err != nil {
		return "", nil, err
	}
	state, err := p.updateHistory(ctx, repoPath, account, sliceSlug, slice)
	if err != nil {
		return "", nil, err
	}
	if state.GitHead != "" && p.publishedHead(repoPath) != state.NativeHeadID {
		p.setPublished(repoPath, state.NativeHeadID)
		p.mirror.Publish(ctx, account, sliceSlug, repoPath, state)
		settle = true
	}
	return repoPath, state.projection(), nil
}

func (p *Projector) setPublished(repoPath, head string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if head == "" {
		delete(p.published, repoPath)
		return
	}
	if p.published == nil {
		p.published = map[string]string{}
	}
	if _, exists := p.published[repoPath]; !exists && len(p.published) >= maxTrackedRepos {
		for k := range p.published {
			delete(p.published, k)
			break
		}
	}
	p.published[repoPath] = head
}

func (p *Projector) publishedHead(repoPath string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.published[repoPath]
}

func (p *Projector) acquireLock(key string) func() {
	p.mu.Lock()
	if p.locks == nil {
		p.locks = map[string]*refMutex{}
	}
	entry := p.locks[key]
	if entry == nil {
		entry = &refMutex{}
		p.locks[key] = entry
	}
	entry.refs++
	p.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		p.mu.Lock()
		entry.refs--
		if entry.refs == 0 && p.locks[key] == entry {
			delete(p.locks, key)
		}
		p.mu.Unlock()
	}
}

func runGit(ctx context.Context, dir string, env []string, args ...string) error {
	_, err := gitOutput(ctx, dir, env, args...)
	return err
}

func gitOutput(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}
