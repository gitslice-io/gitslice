package gitcompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A projection is expensive to build from nothing: it replays a slice's whole
// native history (about 85 ms per commit, so 40 s for 450 commits), and the
// cache lives on one server instance's disk. Every new instance, including each
// deploy, paid that on the first Git request for every slice.
//
// A Mirror keeps a copy of a slice's projected repository in a Git remote that
// outlives the instance, and a new instance restores from it before it falls
// back to building:
//
//	refs/heads/main            the projected history, exactly as served
//	refs/heads/gitslice-state  one commit holding gitslice_projection.json
//
// The state file is what lets the projector extend a cache instead of
// replaying history, so a restored instance only projects the commits that
// landed since the mirror was last pushed. Both refs are plain Git, so a
// mirror can be any Git remote, including a Cloudflare Artifacts repository.
//
// Mirroring is opt-in per slice, because it copies the slice's contents to
// the mirror. A Git remote has to hold the whole history, so this suits slices
// that fit one repository; see design/23 for very large slices.

const (
	mirrorStateBranch = "refs/heads/gitslice-state"
	mirrorStateLocal  = "refs/gitslice/mirror-state"
	// A burst of landings becomes one push per interval.
	mirrorPushInterval = 5 * time.Second
	mirrorPushTimeout  = 10 * time.Minute
	mirrorPushRetries  = 3
	// How long a request that changed the head waits for the mirror to catch
	// up, while the instance still has CPU.
	mirrorSettleWait   = 20 * time.Second
	mirrorRestoreLimit = 3 * time.Minute
)

// Mirror keeps a copy of slices' projections where a new instance can find it.
// Two kinds exist: GitMirror pushes to a Git remote, which suits slices that fit
// one repository, and PackMirror stores the repository's packs in the object
// store, which has no size limit.
type Mirror interface {
	// Enabled reports whether the slice is mirrored.
	Enabled(account, slice string) bool
	// Restore builds repoPath from the mirror. It reports false when there is
	// nothing usable to restore from; the caller then builds from scratch.
	Restore(ctx context.Context, account, slice, repoPath string) (bool, error)
	// Publish copies the projection at repoPath to the mirror in the
	// background. The caller holds the repository lock, which makes the
	// snapshot consistent; the copy runs without it.
	Publish(ctx context.Context, account, slice, repoPath string, state *projectionState)
	// Settle waits, up to a limit, for the publishing started by Publish to
	// finish, and reports its failure. Cloud Run only gives a container CPU
	// while it serves a request, so the caller holds its request open for it.
	Settle(ctx context.Context, account, slice string) error
}

// enabledSlices reads a comma separated list of "account/slice" names; "*"
// means every slice.
func enabledSlices(slices []string) (set map[string]bool, all bool) {
	set = map[string]bool{}
	for _, s := range slices {
		switch s = strings.TrimSpace(s); s {
		case "":
		case "*":
			all = true
		default:
			set[s] = true
		}
	}
	return set, all
}

// publisher runs one background publish per slice at a time. While one is
// running, later snapshots replace each other, so a burst of landings becomes
// at most one more run. A failed run is tried again, unless a newer snapshot
// has replaced it.
//
// On Cloud Run a container only gets CPU while it serves a request, so a
// publish left to itself stalls once the request that started it ends. A caller
// that can spare the time calls wait while it still holds a request open.
type publisher[T any] struct {
	mu       sync.Mutex
	jobs     map[string]*publishJob[T]
	interval time.Duration
	retries  int
	run      func(ctx context.Context, key string, snapshot T) error
	label    string
}

type publishJob[T any] struct {
	pending *T
	running bool
	// submitted counts snapshots handed in; finished is the highest one a run
	// has covered (snapshots that were replaced count as covered by the run
	// that took the newer one). changed is closed, and replaced, on every run.
	submitted, finished int
	lastErr             error
	changed             chan struct{}
}

func newPublisher[T any](label string, run func(ctx context.Context, key string, snapshot T) error) *publisher[T] {
	return &publisher[T]{jobs: map[string]*publishJob[T]{}, interval: mirrorPushInterval, retries: mirrorPushRetries, run: run, label: label}
}

func (p *publisher[T]) submit(key string, snapshot T) {
	p.mu.Lock()
	job := p.jobs[key]
	if job == nil {
		job = &publishJob[T]{changed: make(chan struct{})}
		p.jobs[key] = job
	}
	job.pending = &snapshot
	job.submitted++
	start := !job.running
	job.running = true
	p.mu.Unlock()
	if start {
		go p.loop(key, job)
	}
}

// wait blocks until a run has covered everything submitted for key so far, the
// context ends, or the timeout passes. It returns that run's error. A timeout
// is not an error: the publish carries on in the background.
func (p *publisher[T]) wait(ctx context.Context, key string, timeout time.Duration) error {
	p.mu.Lock()
	job := p.jobs[key]
	if job == nil {
		p.mu.Unlock()
		return nil
	}
	target := job.submitted
	p.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		p.mu.Lock()
		if job.finished >= target {
			err := job.lastErr
			p.mu.Unlock()
			return err
		}
		changed := job.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *publisher[T]) loop(key string, job *publishJob[T]) {
	failures := 0
	for {
		p.mu.Lock()
		snapshot := job.pending
		covers := job.submitted
		job.pending = nil
		if snapshot == nil {
			job.running = false
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), mirrorPushTimeout)
		started := time.Now()
		err := p.run(ctx, key, *snapshot)
		cancel()
		if err != nil {
			slog.Warn(p.label+" failed", "repo", key+".git", "error", err)
			recordGitMirror("publish", "error")
		} else {
			failures = 0
			slog.Info(p.label, "repo", key+".git", "total_ms", time.Since(started).Milliseconds())
			recordGitMirror("publish", "ok")
		}
		p.mu.Lock()
		retry := err != nil && job.pending == nil && failures < p.retries
		if retry {
			failures++
			job.pending = snapshot
		}
		if !retry {
			job.finished = covers
			job.lastErr = err
			close(job.changed)
			job.changed = make(chan struct{})
		}
		more := job.pending != nil
		p.mu.Unlock()
		if more {
			// Debounces a burst of landings; after a failure it is also the backoff.
			time.Sleep(p.interval * time.Duration(1<<failures))
		}
	}
}

// MirrorRemote is where a slice's mirror lives and how to authenticate.
type MirrorRemote struct {
	URL string
	// Header is an HTTP header sent with every Git request (for example
	// "Authorization: Bearer ..."), or "".
	Header string
}

// MirrorBackend finds, and creates if needed, the remote for a slice.
type MirrorBackend interface {
	Remote(ctx context.Context, account, slice string) (MirrorRemote, error)
}

// GitMirror mirrors to a Git remote.
type GitMirror struct {
	backend MirrorBackend
	slices  map[string]bool
	all     bool
	pub     *publisher[mirrorSnapshot]
}

// mirrorSnapshot is what one push sends: the projected head and the commit
// holding the state that describes it.
type mirrorSnapshot struct {
	account, slice, repoPath string
	main, state              string
	nativeHead               string
}

// NewGitMirror mirrors the given "account/slice" names to backend.
func NewGitMirror(backend MirrorBackend, slices []string) *GitMirror {
	set, all := enabledSlices(slices)
	m := &GitMirror{backend: backend, slices: set, all: all}
	m.pub = newPublisher("git projection mirrored", func(ctx context.Context, _ string, s mirrorSnapshot) error { return m.push(ctx, &s) })
	return m
}

func (m *GitMirror) Enabled(account, slice string) bool {
	return m != nil && (m.all || m.slices[account+"/"+slice])
}

func (m *GitMirror) Restore(ctx context.Context, account, slice, repoPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, mirrorRestoreLimit)
	defer cancel()
	started := time.Now()
	remote, err := m.backend.Remote(ctx, account, slice)
	if err != nil {
		return false, fmt.Errorf("mirror remote: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(repoPath), filepath.Base(repoPath)+".restore-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", tmp); err != nil {
		return false, err
	}
	if err := configureProjectedRepo(ctx, tmp); err != nil {
		return false, err
	}
	fetch := []string{"fetch", "--quiet", "--no-tags", remote.URL, "+" + projectedBranch + ":" + projectedBranch, "+" + mirrorStateBranch + ":" + mirrorStateLocal}
	if out, err := gitOutput(ctx, tmp, remote.env(), fetch...); err != nil {
		if strings.Contains(out+err.Error(), "couldn't find remote ref") {
			return false, nil // nothing mirrored yet
		}
		return false, err
	}
	raw, err := gitOutput(ctx, tmp, nil, "show", mirrorStateLocal+":"+projectionStateFile)
	if err != nil {
		return false, fmt.Errorf("mirror has no state: %w", err)
	}
	var state projectionState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return false, fmt.Errorf("mirror state: %w", err)
	}
	head, err := gitOutput(ctx, tmp, nil, "rev-parse", "--verify", projectedBranch)
	if err != nil {
		return false, err
	}
	// The two refs are pushed one after the other; use them only if they
	// describe the same projection.
	if state.Version != projectionVersion || state.Account != account || state.Slice != slice || state.GitHead != strings.TrimSpace(head) {
		return false, errors.New("mirror state does not match its history")
	}
	if err := writeProjectionState(tmp, &state); err != nil {
		return false, err
	}
	if err := os.RemoveAll(repoPath); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, repoPath); err != nil {
		return false, err
	}
	slog.Info("git projection restored from mirror", "repo", account+"/"+slice+".git", "native_commits", len(state.Commits), "total_ms", time.Since(started).Milliseconds())
	return true, nil
}

func (m *GitMirror) Publish(ctx context.Context, account, slice, repoPath string, state *projectionState) {
	snapshot, err := snapshotProjection(ctx, account, slice, repoPath, state)
	if err != nil {
		slog.Warn("git mirror snapshot failed", "repo", account+"/"+slice+".git", "error", err)
		return
	}
	m.pub.submit(account+"/"+slice, *snapshot)
}

func (m *GitMirror) Settle(ctx context.Context, account, slice string) error {
	return m.pub.wait(ctx, account+"/"+slice, mirrorSettleWait)
}

func (m *GitMirror) push(ctx context.Context, s *mirrorSnapshot) error {
	remote, err := m.backend.Remote(ctx, s.account, s.slice)
	if err != nil {
		return fmt.Errorf("mirror remote: %w", err)
	}
	// Pushed by commit id, so a landing that moves the local branch while this
	// runs cannot split the two refs. Forced: a rebuilt history is a new line.
	_, err = gitOutput(ctx, s.repoPath, remote.env(), "push", "--quiet", "--force", remote.URL,
		s.main+":"+projectedBranch, s.state+":"+mirrorStateBranch)
	return err
}

// snapshotProjection writes the state file into a commit and returns the ids
// to push.
func snapshotProjection(ctx context.Context, account, slice, repoPath string, state *projectionState) (*mirrorSnapshot, error) {
	if state.GitHead == "" {
		return nil, errors.New("empty history")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	env := []string{
		"GIT_AUTHOR_NAME=gitslice", "GIT_AUTHOR_EMAIL=mirror@" + noreplyDomain, "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=gitslice", "GIT_COMMITTER_EMAIL=mirror@" + noreplyDomain, "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	blob, err := gitStdin(ctx, repoPath, nil, raw, "hash-object", "-w", "--stdin")
	if err != nil {
		return nil, err
	}
	tree, err := gitStdin(ctx, repoPath, nil, []byte("100644 blob "+blob+"\t"+projectionStateFile+"\n"), "mktree")
	if err != nil {
		return nil, err
	}
	commit, err := gitOutput(ctx, repoPath, env, "commit-tree", tree, "-m", "Projection state at "+state.NativeHeadID)
	if err != nil {
		return nil, err
	}
	return &mirrorSnapshot{account: account, slice: slice, repoPath: repoPath, main: state.GitHead, state: strings.TrimSpace(commit), nativeHead: state.NativeHeadID}, nil
}

func gitStdin(ctx context.Context, dir string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// env passes the remote's header to git without putting a token on the
// command line.
func (r MirrorRemote) env() []string {
	if r.Header == "" {
		return nil
	}
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + r.Header}
}

// DirMirror mirrors into bare repositories under a directory: a shared volume
// in development and in tests.
type DirMirror struct{ root string }

func NewDirMirror(root string) *DirMirror { return &DirMirror{root: root} }

func (d *DirMirror) Remote(ctx context.Context, account, slice string) (MirrorRemote, error) {
	path := filepath.Join(d.root, account, slice+".git")
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err != nil {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return MirrorRemote{}, err
		}
		if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", path); err != nil {
			return MirrorRemote{}, err
		}
	}
	return MirrorRemote{URL: path}, nil
}
