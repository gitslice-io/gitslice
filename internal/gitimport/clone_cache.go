package gitimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// CloneTimeout bounds one clone or fetch. It runs apart from the request
	// that asked for it, so a client that drops does not waste it.
	CloneTimeout = 15 * time.Minute
	// CloneFetchAfter is how old a kept clone may be before an import fetches
	// it again; within it, retries of the same import use it as is.
	CloneFetchAfter = 30 * time.Second
	// CloneStaleOK is how old a kept clone may be and still serve an import
	// when fetching fails.
	CloneStaleOK = 15 * time.Minute
	// CloneIdleTTL is how long an unused clone is kept. On Cloud Run /tmp is
	// memory, so clones do not linger.
	CloneIdleTTL = 20 * time.Minute
	CloneMaxKept = 3
)

// CloneCache keeps bare clones of repositories being imported, so an import
// that resumes after its client dropped starts from a kept clone and a fetch
// instead of cloning the whole history again.
type CloneCache struct {
	mu       sync.Mutex
	root     string
	entries  map[string]*cachedClone
	now      func() time.Time
	run      func(ctx context.Context, dir, allowProtocol string, args ...string) error
	maxKept  int
	idleTTL  time.Duration
	sweeping bool
}

type cachedClone struct {
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

// NewCloneCache returns a new bare-clone cache with default retention limits.
func NewCloneCache() *CloneCache {
	return &CloneCache{
		entries: map[string]*cachedClone{},
		now:     time.Now,
		run:     Run,
		maxKept: CloneMaxKept,
		idleTTL: CloneIdleTTL,
	}
}

// Acquire returns a bare clone of source, current to within CloneFetchAfter,
// and a release func the caller must call when done reading it.
func (c *CloneCache) Acquire(ctx context.Context, source string, local bool) (string, func(), error) {
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
		e = &cachedClone{key: key, dir: filepath.Join(c.root, key)}
		c.entries[key] = e
		c.startLocked(e, source, local, false)
	} else if !e.busy && (e.err != nil || now.Sub(e.fetchedAt) > CloneFetchAfter) {
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
		// A clone that serves nothing yet fails the import. A failed fetch of a
		// kept clone still serves it while it is recent enough.
		if !cloned || c.now().Sub(fetchedAt) > CloneStaleOK {
			c.release(e)
			return "", nil, err
		}
		slog.Warn("git import fetch failed; using the kept clone", "error", err, "fetched_at", fetchedAt)
	}
	return e.dir, func() { c.release(e) }, nil
}

// startLocked clones (or, for a kept clone, fetches) in the background.
func (c *CloneCache) startLocked(e *cachedClone, source string, local, fetch bool) {
	e.busy = true
	e.ready = make(chan struct{})
	allowProtocol := "https"
	if local {
		allowProtocol = "file"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), CloneTimeout)
		defer cancel()
		var err error
		if fetch {
			err = c.run(ctx, e.dir, allowProtocol, "-c", "http.followRedirects=false", "fetch", "--quiet", "--prune", "--force", "--", source, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		} else {
			_ = os.RemoveAll(e.dir)
			args := []string{"-c", "http.followRedirects=false", "clone", "--quiet", "--bare"}
			if !local {
				args = append(args, "--no-local")
			}
			err = c.run(ctx, "", allowProtocol, append(args, "--", source, e.dir)...)
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

func (c *CloneCache) release(e *cachedClone) {
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

func (c *CloneCache) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweeping = false
	c.evictLocked()
	if len(c.entries) > 0 {
		c.sweeping = true
		time.AfterFunc(c.idleTTL, c.sweep)
	}
}

// evictLocked drops clones nobody uses that sat idle past idleTTL, failed, or
// exceed maxKept (oldest first).
func (c *CloneCache) evictLocked() {
	now := c.now()
	var idle []*cachedClone
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

func (c *CloneCache) dropLocked(e *cachedClone) {
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

// Clone creates an ephemeral bare clone of source for a single import and
// returns its directory and a cleanup function.
func Clone(ctx context.Context, source, mode string, maxCommits int, local bool) (string, func(), error) {
	parent, err := os.MkdirTemp("", "gitslice-import-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(parent) }
	repoDir := path.Join(parent, "repo")
	// Bare: the import reads objects (ls-tree, cat-file), never a checkout,
	// and on Cloud Run /tmp is memory.
	args := []string{"-c", "http.followRedirects=false", "clone", "--quiet", "--bare"}
	// Remote clones are pinned to the git transport (no local optimization) and
	// the https protocol so a source that slipped past validation still cannot
	// reach file/ext/ssh. A local clone is an explicit operator opt-in, so it
	// keeps git's local-path handling and allows only the file protocol.
	allowProtocol := "https"
	if local {
		allowProtocol = "file"
	} else {
		args = append(args, "--no-local")
	}
	if mode == "shallow" {
		args = append(args, "--depth", "1")
	} else if maxCommits > 0 {
		args = append(args, "--depth", strconv.Itoa(maxCommits))
	}
	args = append(args, "--", source, repoDir)
	if err := Run(ctx, "", allowProtocol, args...); err != nil {
		cleanup()
		return "", nil, err
	}
	return repoDir, cleanup, nil
}

// CloneError maps a failed clone error to a gRPC status: a source that does
// not exist or needs credentials is InvalidArgument; anything else (a dropped
// connection, a timeout) is Unavailable so the client retries.
func CloneError(ctx context.Context, err error) error {
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
