package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testGitSource makes a local repository with one commit and returns its
// path and a func that adds a commit.
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
	*importCloneCache
	clones, fetches atomic.Int32
	clock           time.Time
	mu              sync.Mutex
}

func newCountingCache(t *testing.T) *countingCache {
	c := &countingCache{importCloneCache: newImportCloneCache(), clock: time.Now()}
	c.now = func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.clock }
	run := c.run
	c.run = func(ctx context.Context, dir, allow string, args ...string) error {
		switch args[0] {
		case "clone":
			c.clones.Add(1)
		case "fetch":
			c.fetches.Add(1)
		}
		return run(ctx, dir, allow, args...)
	}
	t.Cleanup(func() {
		c.importCloneCache.mu.Lock()
		root := c.root
		c.importCloneCache.mu.Unlock()
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

func TestImportCloneCacheReusesAndFetches(t *testing.T) {
	source, commit := testGitSource(t)
	c := newCountingCache(t)
	ctx := context.Background()

	dir, release, err := c.acquire(ctx, source, true)
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

	// A retry soon after uses the kept clone as it is.
	commit("second")
	dir2, release, err := c.acquire(ctx, source, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if dir2 != dir || c.clones.Load() != 1 || c.fetches.Load() != 0 {
		t.Fatalf("clones %d fetches %d, want one clone reused", c.clones.Load(), c.fetches.Load())
	}

	// Later, it fetches first and sees the new commit.
	c.advance(importCloneFetchAfter + time.Second)
	dir3, release, err := c.acquire(ctx, source, true)
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

// A client that drops while the clone runs does not waste it: the clone
// finishes, and the retry uses it.
func TestImportCloneCacheCloneOutlivesItsCaller(t *testing.T) {
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
		_, _, err := c.acquire(ctx, source, true)
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire error = %v, want canceled", err)
	}
	close(gate)

	dir, release, err := c.acquire(context.Background(), source, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if c.clones.Load() != 1 || headSubject(t, dir) != "first" {
		t.Fatalf("clones = %d, want the first clone reused", c.clones.Load())
	}
}

func TestImportCloneCacheConcurrentImportsShareOneClone(t *testing.T) {
	source, _ := testGitSource(t)
	c := newCountingCache(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := c.acquire(context.Background(), source, true)
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

func TestImportCloneCacheFailedCloneIsRetried(t *testing.T) {
	c := newCountingCache(t)
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, err := c.acquire(context.Background(), missing, true); err == nil {
		t.Fatal("clone of a missing repository succeeded")
	}
	if _, _, err := c.acquire(context.Background(), missing, true); err == nil {
		t.Fatal("clone of a missing repository succeeded")
	}
	if c.clones.Load() != 2 {
		t.Fatalf("clones = %d, want a fresh attempt each time", c.clones.Load())
	}
}

// When the fetch of a recent kept clone fails, imports keep using it.
func TestImportCloneCacheServesKeptCloneWhenFetchFails(t *testing.T) {
	source, _ := testGitSource(t)
	c := newCountingCache(t)
	_, release, err := c.acquire(context.Background(), source, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	run := c.run
	c.run = func(ctx context.Context, dir, allow string, args ...string) error {
		if args[0] == "fetch" {
			return errors.New("fetch: early EOF")
		}
		return run(ctx, dir, allow, args...)
	}
	c.advance(importCloneFetchAfter + time.Second)
	_, release, err = c.acquire(context.Background(), source, true)
	if err != nil {
		t.Fatalf("acquire with a failing fetch = %v, want the kept clone", err)
	}
	release()
	c.advance(importCloneStaleOK)
	if _, _, err := c.acquire(context.Background(), source, true); err == nil {
		t.Fatal("a stale kept clone served an import although fetching failed")
	}
}

func TestImportCloneCacheEvictsIdleAndExtraClones(t *testing.T) {
	a, _ := testGitSource(t)
	b, _ := testGitSource(t)
	c := newCountingCache(t)
	c.maxKept = 1
	dirA, release, err := c.acquire(context.Background(), a, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	c.advance(time.Second)
	_, release, err = c.acquire(context.Background(), b, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(dirA); !os.IsNotExist(err) {
		t.Fatalf("the older clone was kept beyond maxKept: %v", err)
	}
	c.advance(c.idleTTL + time.Second)
	c.sweep()
	c.importCloneCache.mu.Lock()
	n := len(c.entries)
	c.importCloneCache.mu.Unlock()
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
		if got := status.Code(cloneError(ctx, errors.New(msg))); got != want {
			t.Errorf("cloneError(%q) = %s, want %s", msg, got, want)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := status.Code(cloneError(canceled, errors.New("signal: killed"))); got != codes.Canceled {
		t.Errorf("cloneError after the client left = %s, want Canceled", got)
	}
}
