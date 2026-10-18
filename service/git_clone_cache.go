package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// importCloneTimeout bounds one clone or fetch. It runs apart from the
	// request that asked for it, so a client that drops does not waste it.
	importCloneTimeout = 15 * time.Minute
	// importCloneFetchAfter is how old a kept clone may be before an import
	// fetches it again; within it, retries of the same import use it as is.
	importCloneFetchAfter = 30 * time.Second
	// importCloneStaleOK is how old a kept clone may be and still serve an
	// import when fetching fails.
	importCloneStaleOK = 15 * time.Minute
	// importCloneIdleTTL is how long an unused clone is kept. On Cloud Run
	// /tmp is memory, so clones do not linger.
	importCloneIdleTTL = 20 * time.Minute
	importCloneMaxKept = 3
)

// importCloneCache keeps bare clones of the repositories being imported, so
// an import that resumes after its client dropped (design/31's flaky links,
// a deploy replacing the instance) starts from a kept clone and a fetch
// instead of cloning the whole history again.
type importCloneCache struct {
	mu       sync.Mutex
	root     string
	entries  map[string]*importClone
	now      func() time.Time
	run      func(ctx context.Context, dir, allowProtocol string, args ...string) error
	maxKept  int
	idleTTL  time.Duration
	sweeping bool
}

type importClone struct {
	key       string
	dir       string
	ready     chan struct{} // closed when the current clone or fetch ends
	err       error         // its error
	cloned    bool          // a clone has completed at least once
	busy      bool
	users     int
	lastUsed  time.Time
	fetchedAt time.Time
}

func newImportCloneCache() *importCloneCache {
	return &importCloneCache{
		entries: map[string]*importClone{},
		now:     time.Now,
		run:     runGitImport,
		maxKept: importCloneMaxKept,
		idleTTL: importCloneIdleTTL,
	}
}

// acquire returns a bare clone of source, current to within
// importCloneFetchAfter, and a release func the caller must call when done
// reading it.
func (c *importCloneCache) acquire(ctx context.Context, source string, local bool) (string, func(), error) {
	c.mu.Lock()
	if c.root == "" {
		root, err := os.MkdirTemp("", "gitslice-import-clones-*")
		if err != nil {
			c.mu.Unlock()
			return "", nil, err
		}
		c.root = root
	}
	key := cloneKey(source, local)
	e := c.entries[key]
	now := c.now()
	if e == nil {
		e = &importClone{key: key, dir: filepath.Join(c.root, key)}
		c.entries[key] = e
		c.startLocked(e, source, local, false)
	} else if !e.busy && (e.err != nil || now.Sub(e.fetchedAt) > importCloneFetchAfter) {
		c.startLocked(e, source, local, e.cloned)
	}
	e.users++
	ready := e.ready
	c.mu.Unlock()

	select {
	case <-ready:
	case <-ctx.Done():
		c.release(e)
		return "", nil, ctx.Err()
	}

	c.mu.Lock()
	err, cloned, fetchedAt := e.err, e.cloned, e.fetchedAt
	c.mu.Unlock()
	if err != nil {
		// A clone that serves nothing yet fails the import. A failed fetch
		// of a kept clone still serves it while it is recent enough.
		if !cloned || c.now().Sub(fetchedAt) > importCloneStaleOK {
			c.release(e)
			return "", nil, err
		}
		slog.Warn("git import fetch failed; using the kept clone", "error", err, "fetched_at", fetchedAt)
	}
	return e.dir, func() { c.release(e) }, nil
}

// startLocked clones (or, for a kept clone, fetches) in the background.
func (c *importCloneCache) startLocked(e *importClone, source string, local, fetch bool) {
	e.busy = true
	e.ready = make(chan struct{})
	allowProtocol := "https"
	if local {
		allowProtocol = "file"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), importCloneTimeout)
		defer cancel()
		var err error
		if fetch {
			err = c.run(ctx, e.dir, allowProtocol, "fetch", "--quiet", "--prune", "--force", source, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		} else {
			_ = os.RemoveAll(e.dir)
			args := []string{"clone", "--quiet", "--bare"}
			if !local {
				args = append(args, "--no-local")
			}
			err = c.run(ctx, "", allowProtocol, append(args, source, e.dir)...)
			if err != nil {
				_ = os.RemoveAll(e.dir)
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		e.err = err
		if err == nil {
			e.cloned = true
			e.fetchedAt = c.now()
		}
		e.busy = false
		close(e.ready)
	}()
}

func (c *importCloneCache) release(e *importClone) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e.users--
	e.lastUsed = c.now()
	c.evictLocked()
	if !c.sweeping && len(c.entries) > 0 {
		c.sweeping = true
		time.AfterFunc(c.idleTTL, c.sweep)
	}
}

func (c *importCloneCache) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweeping = false
	c.evictLocked()
	if len(c.entries) > 0 {
		c.sweeping = true
		time.AfterFunc(c.idleTTL, c.sweep)
	}
}

// evictLocked drops clones nobody uses that sat idle past idleTTL, failed,
// or exceed maxKept (oldest first).
func (c *importCloneCache) evictLocked() {
	now := c.now()
	var idle []*importClone
	for _, e := range c.entries {
		if e.users > 0 || e.busy {
			continue
		}
		if !e.cloned || now.Sub(e.lastUsed) > c.idleTTL {
			c.dropLocked(e)
			continue
		}
		idle = append(idle, e)
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].lastUsed.Before(idle[j].lastUsed) })
	for len(c.entries) > c.maxKept && len(idle) > 0 {
		c.dropLocked(idle[0])
		idle = idle[1:]
	}
}

func (c *importCloneCache) dropLocked(e *importClone) {
	delete(c.entries, e.key)
	_ = os.RemoveAll(e.dir)
}

func cloneKey(source string, local bool) string {
	prefix := "remote\x00"
	if local {
		prefix = "local\x00"
	}
	sum := sha256.Sum256([]byte(prefix + source))
	return hex.EncodeToString(sum[:])[:24]
}

// cloneError is the status for a clone that failed: a source that does not
// exist or needs credentials is the caller's mistake; anything else (a
// dropped connection, a timeout) is worth retrying.
func cloneError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return status.FromContextError(ctxErr).Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.Errorf(codes.Unavailable, "clone git repository: %v", err)
	}
	msg := strings.ToLower(err.Error())
	for _, permanent := range []string{"not found", "authentication failed", "could not read username", "terminal prompts disabled", "does not appear to be a git repository", "unsupported protocol", "transport '"} {
		if strings.Contains(msg, permanent) {
			return status.Errorf(codes.InvalidArgument, "clone git repository: %v", err)
		}
	}
	return status.Errorf(codes.Unavailable, "clone git repository: %v; try again, an import resumes where it stopped", err)
}
