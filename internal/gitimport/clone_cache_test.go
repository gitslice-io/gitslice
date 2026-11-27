package gitimport

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testGitSource(t *testing.T) (string, func(msg string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("init", "-q", "-b", "main")
	commit := func(msg string) {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "f.txt")
		git("commit", "-q", "-m", msg)
	}
	commit("first")
	return dir, commit
}

type countingCache struct {
	*CloneCache
	clones, fetches atomic.Int32
	clock           time.Time
	mu              sync.Mutex
}

func newCountingCache(t *testing.T) *countingCache {
	c := &countingCache{CloneCache: NewCloneCache(), clock: time.Now()}
	c.now = func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.clock }
	run := c.run
	c.run = func(ctx context.Context, dir, allow string, args ...string) error {
		switch {
		case slices.Contains(args, "clone"):
			c.clones.Add(1)
		case slices.Contains(args, "fetch"):
			c.fetches.Add(1)
		}
		return run(ctx, dir, allow, args...)
	}
	t.Cleanup(func() {
		c.CloneCache.mu.Lock()
		root := c.root
		c.CloneCache.mu.Unlock()
		if root != "" {
			_ = os.RemoveAll(root)
		}
	})
	return c
}

func (c *countingCache) advance(d time.Duration) {
	c.mu.Lock()
	c.clock = c.clock.Add(d)
	c.mu.Unlock()
}

func headSubject(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%s", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestCloneCacheReusesAndFetches(t *testing.T) {
	source, commit := testGitSource(t)
	c := newCountingCache(t)
	ctx := context.Background()

	dir, release, err := c.Acquire(ctx, source, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Fatalf("not a bare clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); err == nil {
		t.Fatal("clone has a checkout")
	}
	release()

	commit("second")
	dir2, release, err := c.Acquire(ctx, source, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if dir2 != dir || c.clones.Load() != 1 || c.fetches.Load() != 0 {
		t.Fatalf("clones %d fetches %d, want one clone reused", c.clones.Load(), c.fetches.Load())
	}

	c.advance(CloneFetchAfter + time.Second)
	dir3, release, err := c.Acquire(ctx, source, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if c.clones.Load() != 1 || c.fetches.Load() != 1 {
		t.Fatalf("clones %d fetches %d, want a fetch", c.clones.Load(), c.fetches.Load())
	}
	if got := headSubject(t, dir3); got != "second" {
		t.Fatalf("HEAD is %q after fetch, want second", got)
	}
}

func TestCloneCacheCloneOutlivesItsCaller(t *testing.T) {
	source, _ := testGitSource(t)
	c := newCountingCache(t)
	gate := make(chan struct{})
	run := c.run
	c.run = func(ctx context.Context, dir, allow string, args ...string) error {
		<-gate
		return run(ctx, dir, allow, args...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := c.Acquire(ctx, source, true)
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire error = %v, want canceled", err)
	}
	close(gate)

	dir, release, err := c.Acquire(context.Background(), source, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if c.clones.Load() != 1 || headSubject(t, dir) != "first" {
		t.Fatalf("clones = %d, want the first clone reused", c.clones.Load())
	}
}

func TestCloneCacheConcurrentImportsShareOneClone(t *testing.T) {
	source, _ := testGitSource(t)
	c := newCountingCache(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := c.Acquire(context.Background(), source, true)
			if err != nil {
				t.Error(err)
				return
			}
			release()
		}()
	}
	wg.Wait()
	if c.clones.Load() != 1 {
		t.Fatalf("clones = %d, want 1", c.clones.Load())
	}
}

func TestCloneCacheFailedCloneIsRetried(t *testing.T) {
	c := newCountingCache(t)
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, err := c.Acquire(context.Background(), missing, true); err == nil {
		t.Fatal("clone of a missing repository succeeded")
	}
	if _, _, err := c.Acquire(context.Background(), missing, true); err == nil {
		t.Fatal("clone of a missing repository succeeded")
	}
	if c.clones.Load() != 2 {
		t.Fatalf("clones = %d, want a fresh attempt each time", c.clones.Load())
	}
}

func TestCloneCacheServesKeptCloneWhenFetchFails(t *testing.T) {
	source, _ := testGitSource(t)
	c := newCountingCache(t)
	_, release, err := c.Acquire(context.Background(), source, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	run := c.run
	c.run = func(ctx context.Context, dir, allow string, args ...string) error {
		if slices.Contains(args, "fetch") {
			return errors.New("fetch: early EOF")
		}
		return run(ctx, dir, allow, args...)
	}
	c.advance(CloneFetchAfter + time.Second)
	_, release, err = c.Acquire(context.Background(), source, true)
	if err != nil {
		t.Fatalf("acquire with a failing fetch = %v, want the kept clone", err)
	}
	release()
	c.advance(CloneStaleOK)
	if _, _, err := c.Acquire(context.Background(), source, true); err == nil {
		t.Fatal("a stale kept clone served an import although fetching failed")
	}
}

func TestCloneCacheEvictsIdleAndExtraClones(t *testing.T) {
	a, _ := testGitSource(t)
	b, _ := testGitSource(t)
	c := newCountingCache(t)
	c.maxKept = 1
	dirA, release, err := c.Acquire(context.Background(), a, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	c.advance(time.Second)
	_, release, err = c.Acquire(context.Background(), b, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(dirA); !os.IsNotExist(err) {
		t.Fatalf("the older clone was kept beyond maxKept: %v", err)
	}
	c.advance(c.idleTTL + time.Second)
	c.sweep()
	c.CloneCache.mu.Lock()
	n := len(c.entries)
	c.CloneCache.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d clones kept after idling, want 0", n)
	}
}

func TestCloneErrorSaysWhetherToRetry(t *testing.T) {
	ctx := context.Background()
	cases := map[string]codes.Code{
		"git clone: fatal: early EOF":                         codes.Unavailable,
		"git clone: signal: killed":                           codes.Unavailable,
		"git clone: remote: Repository not found.":            codes.InvalidArgument,
		"git clone: fatal: Authentication failed for 'https'": codes.InvalidArgument,
	}
	for msg, want := range cases {
		if got := status.Code(CloneError(ctx, errors.New(msg))); got != want {
			t.Errorf("CloneError(%q) = %s, want %s", msg, got, want)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := status.Code(CloneError(canceled, errors.New("signal: killed"))); got != codes.Canceled {
		t.Errorf("CloneError after the client left = %s, want Canceled", got)
	}
}

func TestHardeningFlagsBlobCapsAndResilience(t *testing.T) {
	source, commit := testGitSource(t)
	c := newCountingCache(t)
	var recorded [][]string
	run := c.run
	c.run = func(ctx context.Context, dir, allow string, args ...string) error {
		c.mu.Lock()
		recorded = append(recorded, append([]string(nil), args...))
		c.mu.Unlock()
		return run(ctx, dir, allow, args...)
	}

	dir, release, err := c.Acquire(context.Background(), source, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	commit("second")
	c.advance(CloneFetchAfter + time.Second)
	dir, release, err = c.Acquire(context.Background(), source, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if len(recorded) < 2 {
		t.Fatalf("recorded %d git commands, want clone and fetch", len(recorded))
	}
	for _, args := range recorded {
		if !slices.Contains(args, "http.followRedirects=false") {
			t.Fatalf("git args %v missing http.followRedirects=false", args)
		}
		if !slices.Contains(args, "--") {
			t.Fatalf("git args %v missing -- separator", args)
		}
	}

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD:f.txt").Output()
	if err != nil {
		t.Fatal(err)
	}
	blobID := strings.TrimSpace(string(out))

	prevBlobMax, prevCommitMax := MaxBlobBytes, MaxCommitBytes
	t.Cleanup(func() {
		MaxBlobBytes = prevBlobMax
		MaxCommitBytes = prevCommitMax
	})

	MaxBlobBytes = 2
	if _, err := BlobContents(context.Background(), dir, []string{blobID}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("BlobContents over single-blob cap = %v, want ResourceExhausted", err)
	}

	MaxBlobBytes = 64
	MaxCommitBytes = 8
	if _, err := BlobContents(context.Background(), dir, []string{blobID, blobID}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("BlobContents over commit cap = %v, want ResourceExhausted", err)
	}

	// Progress ignores a disconnected client and emits heartbeats.
	var sent atomic.Int32
	p := NewProgress(func(*corev1.ImportGitRepositoryProgress) error {
		if sent.Add(1) > 1 {
			return errors.New("closed")
		}
		return nil
	})
	_ = p.Emit(&corev1.ImportGitRepositoryProgress{Phase: "published", Current: 1})
	_ = p.Emit(&corev1.ImportGitRepositoryProgress{Phase: "published", Current: 2})
	_ = p.Emit(&corev1.ImportGitRepositoryProgress{Phase: "published", Current: 3})
	if sent.Load() != 2 {
		t.Fatalf("sent = %d, want 2", sent.Load())
	}
}
