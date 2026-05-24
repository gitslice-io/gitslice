package gitcompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type ObjectStore interface {
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
}

type Projector struct {
	auth        *postgres.AuthStore
	repository  *postgres.RepositoryStore
	slices      *postgres.SliceStore
	objectStore ObjectStore
	cacheRoot   string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

type ProjectorStores struct {
	Auth       *postgres.AuthStore
	Repository *postgres.RepositoryStore
	Slices     *postgres.SliceStore
}

type Projection struct {
	Account        string `json:"account"`
	Slice          string `json:"slice"`
	SliceID        string `json:"slice_id"`
	DefinitionHash string `json:"definition_hash"`
	NativeCommitID string `json:"native_commit_id"`
	GitCommitID    string `json:"git_commit_id"`
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
		objectStore: objectStore,
		cacheRoot:   cacheRoot,
		locks:       map[string]*sync.Mutex{},
	}, nil
}

func (p *Projector) CacheRoot() string {
	return p.cacheRoot
}

func (p *Projector) AuthorizeSlice(ctx context.Context, subjectID, account, sliceSlug string) error {
	if err := p.auth.EnsureAccountMember(ctx, subjectID, account); err != nil {
		return err
	}
	_, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	return err
}

func (p *Projector) EnsureProjectedRepo(ctx context.Context, subjectID, account, sliceSlug string) (string, *Projection, error) {
	if err := p.auth.EnsureAccountMember(ctx, subjectID, account); err != nil {
		return "", nil, err
	}
	slice, err := p.slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return "", nil, err
	}
	ref, err := p.repository.GetRef(ctx, postgres.DefaultTargetRef)
	if err != nil {
		return "", nil, err
	}
	commit, err := p.repository.GetCommit(ctx, ref.CommitId)
	if err != nil {
		return "", nil, err
	}
	repoPath := filepath.Join(p.cacheRoot, account, sliceSlug+".git")
	projection := &Projection{
		Account:        account,
		Slice:          sliceSlug,
		SliceID:        slice.Id,
		DefinitionHash: slice.DefinitionHash,
		NativeCommitID: ref.CommitId,
	}
	lock := p.lockFor(repoPath)
	lock.Lock()
	defer lock.Unlock()

	existing, err := readProjection(repoPath)
	if err == nil && projectionMatches(existing, projection) {
		return repoPath, existing, nil
	}
	if err := p.rebuild(ctx, repoPath, slice, commit, projection); err != nil {
		return "", nil, err
	}
	return repoPath, projection, nil
}

func (p *Projector) rebuild(ctx context.Context, repoPath string, slice *corev1.Slice, commit *corev1.Commit, projection *Projection) error {
	if _, err := os.Stat(repoPath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
			return err
		}
		if err := runGit(ctx, "", nil, "init", "--bare", repoPath); err != nil {
			return err
		}
		if err := runGit(ctx, repoPath, nil, "config", "http.receivepack", "false"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := runGit(ctx, repoPath, nil, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	worktree, err := os.MkdirTemp(filepath.Dir(repoPath), ".projection-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(worktree)
	if err := runGit(ctx, "", nil, "init", worktree); err != nil {
		return err
	}
	if err := runGit(ctx, worktree, nil, "checkout", "-B", "main"); err != nil {
		return err
	}
	files, err := p.projectedFiles(ctx, slice, commit.Id)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := p.writeFile(ctx, worktree, file); err != nil {
			return err
		}
	}
	if err := runGit(ctx, worktree, nil, "add", "-A"); err != nil {
		return err
	}
	commitTime := parseCommitTime(commit.CreatedAt)
	env := []string{
		"GIT_AUTHOR_NAME=Gitslice",
		"GIT_AUTHOR_EMAIL=gitslice@example.invalid",
		"GIT_COMMITTER_NAME=Gitslice",
		"GIT_COMMITTER_EMAIL=gitslice@example.invalid",
		"GIT_AUTHOR_DATE=" + commitTime,
		"GIT_COMMITTER_DATE=" + commitTime,
	}
	message := commit.Message
	if message == "" {
		message = "Project " + commit.Id
	}
	if err := runGit(ctx, worktree, env, "commit", "--allow-empty", "-m", message); err != nil {
		return err
	}
	gitCommitID, err := gitOutput(ctx, worktree, nil, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	projection.GitCommitID = strings.TrimSpace(gitCommitID)
	if err := runGit(ctx, worktree, nil, "push", "--force", repoPath, "HEAD:refs/heads/main"); err != nil {
		return err
	}
	return writeProjection(repoPath, projection)
}

func (p *Projector) projectedFiles(ctx context.Context, slice *corev1.Slice, commitID string) ([]postgres.FileEntry, error) {
	byPath := map[string]postgres.FileEntry{}
	for _, prefix := range slice.Definition.IncludedPaths {
		canonical, err := paths.Canonical(prefix)
		if err != nil {
			return nil, err
		}
		files, err := p.repository.ListFiles(ctx, commitID, canonical)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			byPath[file.Path] = file
		}
	}
	out := make([]postgres.FileEntry, 0, len(byPath))
	for _, file := range byPath {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (p *Projector) writeFile(ctx context.Context, worktree string, file postgres.FileEntry) error {
	rel := strings.TrimPrefix(file.Path, "/")
	target := filepath.Join(worktree, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := p.objectStore.Get(ctx, filesystem.BlobKey(file.ContentHash), 0, 0)
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(file.Mode&0o777))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (p *Projector) lockFor(key string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	lock := p.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		p.locks[key] = lock
	}
	return lock
}

func readProjection(repoPath string) (*Projection, error) {
	data, err := os.ReadFile(filepath.Join(repoPath, "gitslice_projection.json"))
	if err != nil {
		return nil, err
	}
	var projection Projection
	if err := json.Unmarshal(data, &projection); err != nil {
		return nil, err
	}
	return &projection, nil
}

func writeProjection(repoPath string, projection *Projection) error {
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(repoPath, "gitslice_projection.json"), data, 0o644)
}

func projectionMatches(existing, wanted *Projection) bool {
	return existing.Account == wanted.Account &&
		existing.Slice == wanted.Slice &&
		existing.SliceID == wanted.SliceID &&
		existing.DefinitionHash == wanted.DefinitionHash &&
		existing.NativeCommitID == wanted.NativeCommitID
}

func parseCommitTime(value string) string {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return time.Unix(0, 0).UTC().Format(time.RFC3339)
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
