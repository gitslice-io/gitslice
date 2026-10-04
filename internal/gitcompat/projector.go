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

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
)

type ObjectStore interface {
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
}

type Projector struct {
	auth        storage.AuthStore
	repository  storage.RepositoryStore
	slices      storage.SliceStore
	objectStore ObjectStore
	cacheRoot   string
	blobs       storage.BlobStore
	lazy        bool

	mu    sync.Mutex
	locks map[string]*sync.Mutex

	// mirror, when set, keeps a copy of mirrored slices' projections outside
	// this instance. published records the native head last handed to it.
	mirror    Mirror
	published map[string]string
	// complete records, per repository, a head at which every file's contents
	// are present, so a full clone need not search for missing ones again.
	complete map[string]string
}

// SetLazy makes the projector build history from recorded Git blob ids and
// leave file contents out of the repository until a fetch needs them.
func (p *Projector) SetLazy(lazy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lazy = lazy
}

// SetMirror makes the projector restore from, and publish to, a mirror for
// the slices it is enabled for.
func (p *Projector) SetMirror(m Mirror) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mirror = m
	if p.published == nil {
		p.published = map[string]string{}
	}
}

type ProjectorStores struct {
	Auth       storage.AuthStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
	// Blobs supplies the Git blob ids the lazy projection builds trees from.
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
		cacheRoot:   cacheRoot,
		locks:       map[string]*sync.Mutex{},
	}, nil
}

func (p *Projector) CacheRoot() string {
	return p.cacheRoot
}

func (p *Projector) AuthorizeSlice(ctx context.Context, subjectID, account, sliceSlug string) error {
	slice, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return err
	}
	return authz.New(p.auth).Authorize(ctx, subjectID, slice, authz.ActionWrite)
}

func (p *Projector) EnsureProjectedRepo(ctx context.Context, subjectID, account, sliceSlug string) (string, *Projection, error) {
	slice, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return "", nil, err
	}
	if err := authz.New(p.auth).Authorize(ctx, subjectID, slice, authz.ActionRead); err != nil {
		return "", nil, err
	}
	repoPath := filepath.Join(p.cacheRoot, account, sliceSlug+".git")
	lock := p.lockFor(repoPath)
	lock.Lock()
	defer lock.Unlock()

	mirrored := p.mirror != nil && p.mirror.Enabled(account, sliceSlug)
	if mirrored {
		if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); errors.Is(err, os.ErrNotExist) {
			// A cold instance: restore what an earlier one mirrored before
			// replaying the whole history. The restore outlives this request.
			restored, err := p.mirror.Restore(context.WithoutCancel(ctx), account, sliceSlug, repoPath)
			switch {
			case err != nil:
				slog.Warn("git mirror restore failed; building from scratch", "repo", account+"/"+sliceSlug+".git", "error", err)
			case restored:
				if state := loadProjectionState(repoPath); state != nil {
					p.setPublished(repoPath, state.NativeHeadID)
				}
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
	if mirrored && state.GitHead != "" && p.publishedHead(repoPath) != state.NativeHeadID {
		p.setPublished(repoPath, state.NativeHeadID)
		p.mirror.Publish(ctx, account, sliceSlug, repoPath, state)
	}
	return repoPath, state.projection(), nil
}

func (p *Projector) setPublished(repoPath, head string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.published == nil {
		p.published = map[string]string{}
	}
	p.published[repoPath] = head
}

func (p *Projector) publishedHead(repoPath string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.published[repoPath]
}

func (p *Projector) lockFor(key string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.locks == nil {
		p.locks = map[string]*sync.Mutex{}
	}
	lock := p.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		p.locks[key] = lock
	}
	return lock
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
