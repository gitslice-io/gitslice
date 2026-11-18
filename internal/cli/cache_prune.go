package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
)

// The client object cache (~/.cache/gitslice) is shared by every workspace
// and mount on the machine. It is pruned, least recently used first, once it
// grows past a limit (GS_CACHE_MAX, default defaultCacheMax): automatically
// at most once per cachePruneInterval, after a command or from the mount
// supervisor, and on demand with gs cache prune. Content only this machine
// has is pinned: what lazy workspaces' checkpoints and any workspace's sync
// conflicts name (design/30_lazy_workspaces.md, "The client cache").

const (
	defaultCacheMax    int64 = 10 << 30
	cachePruneInterval       = time.Hour
	// cacheMinAge: an object used this recently is never pruned, so a command
	// that saw an object in the cache can still read it.
	cacheMinAge = 6 * time.Hour
)

// objectCacheRoot is where the object cache lives, without creating it.
func (r Runner) objectCacheRoot() string {
	root := os.Getenv("GS_CLIENT_CACHE_DIR")
	if root == "" {
		root = os.Getenv("GITSLICE_CLIENT_CACHE_DIR")
	}
	if root == "" {
		root = r.defaultObjectCacheRoot()
	}
	return root
}

// objectCacheLimit is GS_CACHE_MAX: a size such as 10G, 500M or a number of
// bytes; 0 or off turns pruning off.
func objectCacheLimit() (int64, error) {
	raw := strings.TrimSpace(os.Getenv("GS_CACHE_MAX"))
	if raw == "" {
		return defaultCacheMax, nil
	}
	if raw == "off" {
		return 0, nil
	}
	return parseByteSize(raw)
}

func parseByteSize(raw string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "IB"), "B")
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'K':
			mult, s = 1<<10, s[:n-1]
		case 'M':
			mult, s = 1<<20, s[:n-1]
		case 'G':
			mult, s = 1<<30, s[:n-1]
		case 'T':
			mult, s = 1<<40, s[:n-1]
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid size %q (use a number of bytes, or 500M, 10G)", raw)
	}
	return int64(v * float64(mult)), nil
}

// cachePinsDir holds one pointer per workspace with sync conflicts: the path
// of its conflicts.json, whose local sides may be only in the cache.
func cachePinsDir(root string) string { return filepath.Join(root, "pins") }

// pinConflictContent records where a workspace keeps its conflicts, so a
// prune reads them and keeps the content they name. Failing is not an error:
// the object stays at least cacheMinAge after its last use anyway.
func (r Runner) pinConflictContent(conflictsPath string) {
	abs, err := filepath.Abs(conflictsPath)
	if err != nil {
		return
	}
	dir := cachePinsDir(r.objectCacheRoot())
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	sum := sha256.Sum256([]byte(abs))
	name := filepath.Join(dir, "conflicts-"+hex.EncodeToString(sum[:8]))
	if current, err := os.ReadFile(name); err == nil && string(current) == abs {
		return
	}
	_ = os.WriteFile(name, []byte(abs), 0o600)
}

// cachePins returns the content hashes the cache must keep: lazy workspaces'
// checkpoints and the sides of every recorded sync conflict. A pointer to a
// workspace whose .gs is gone is dropped.
func (r Runner) cachePins() map[string]bool {
	pins := map[string]bool{}
	addConflicts := func(path string) bool {
		var state WorkspaceConflictState
		if err := readJSONFile(path, &state); err != nil {
			return false
		}
		for _, c := range state.Conflicts {
			for _, h := range []string{c.BaseContentHash, c.LocalContentHash, c.RemoteContentHash} {
				if h != "" {
					pins[h] = true
				}
			}
		}
		return true
	}
	if workspaces, err := os.ReadDir(r.mountsRoot()); err == nil {
		for _, ws := range workspaces {
			if !ws.IsDir() || !isValidMountID(ws.Name()) {
				continue
			}
			addConflicts(filepath.Join(r.mountGsDir(ws.Name()), "conflicts.json"))
			checkpoints, _ := r.checkpoints(ws.Name())
			for _, cp := range checkpoints {
				for _, edit := range cp.Edits {
					if edit.ContentHash != "" {
						pins[edit.ContentHash] = true
					}
				}
			}
		}
	}
	dir := cachePinsDir(r.objectCacheRoot())
	pointers, _ := os.ReadDir(dir)
	for _, p := range pointers {
		name := filepath.Join(dir, p.Name())
		target, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if !addConflicts(string(target)) {
			if _, err := os.Stat(filepath.Dir(string(target))); os.IsNotExist(err) {
				_ = os.Remove(name) // the workspace is gone
			}
		}
	}
	return pins
}

// pruneObjectCache prunes the cache to max (objectCacheLimit when max < 0).
func (r Runner) pruneObjectCache(max int64, dryRun bool) (clientcache.PruneResult, int64, error) {
	if max < 0 {
		limit, err := objectCacheLimit()
		if err != nil {
			return clientcache.PruneResult{}, 0, err
		}
		max = limit
	}
	cache, err := r.objectCache()
	if err != nil {
		return clientcache.PruneResult{}, max, err
	}
	pins := r.cachePins()
	res, err := cache.Prune(clientcache.PruneOptions{
		MaxBytes: max,
		MinAge:   cacheMinAge,
		Keep:     func(hash string) bool { return pins[hash] },
		DryRun:   dryRun,
	})
	return res, max, err
}

// maybePruneObjectCache prunes the cache when the last prune was more than
// cachePruneInterval ago. It never creates the cache, and it says nothing:
// a failure leaves the cache as it was.
func (r Runner) maybePruneObjectCache() {
	root := r.objectCacheRoot()
	if _, err := os.Stat(filepath.Join(root, "objects")); err != nil {
		return
	}
	stamp := filepath.Join(root, "prune.stamp")
	if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < cachePruneInterval {
		return
	}
	if f, err := os.OpenFile(stamp, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
	now := time.Now()
	_ = os.Chtimes(stamp, now, now)
	_, _, _ = r.pruneObjectCache(-1, false)
}

func (r Runner) runCacheStatus(opts commandOptions) error {
	limit, err := objectCacheLimit()
	if err != nil {
		return userError("invalid_cache_max", err.Error(), "Set GS_CACHE_MAX to a size such as 10G, or off.")
	}
	cache, err := r.objectCache()
	if err != nil {
		return err
	}
	objects, bytes, err := cache.Usage()
	if err != nil {
		return err
	}
	pins := r.cachePins()
	out := map[string]any{"path": cache.Root(), "objects": objects, "bytes": bytes, "max_bytes": limit, "pinned": len(pins)}
	if opts.Format == "json" {
		return r.writeJSONOutput(opts, out)
	}
	max := "off"
	if limit > 0 {
		max = humanBytes(limit)
	}
	fmt.Fprintf(r.Stdout, "cache: %s\nobjects: %d (%s)\nlimit: %s (GS_CACHE_MAX)\npinned: %d (checkpoints and sync conflicts; never pruned)\n", cache.Root(), objects, humanBytes(bytes), max, len(pins))
	return nil
}

func (r Runner) runCachePrune(opts commandOptions, maxFlag string, dryRun bool) error {
	max := int64(-1)
	if maxFlag != "" {
		v, err := parseByteSize(maxFlag)
		if err != nil {
			return userError("invalid_cache_max", err.Error(), "Pass --max as a size such as 2G or 500M.")
		}
		max = v
	}
	res, limit, err := r.pruneObjectCache(max, dryRun)
	if err != nil {
		return err
	}
	if opts.Format == "json" {
		return r.writeJSONOutput(opts, map[string]any{"objects": res.Objects, "bytes": res.Bytes, "max_bytes": limit, "removed": res.Removed, "removed_bytes": res.RemovedBytes, "pinned": res.Kept, "dry_run": dryRun})
	}
	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	switch {
	case limit <= 0:
		fmt.Fprintf(r.Stdout, "cache limit is off; %d objects (%s) kept\n", res.Objects, humanBytes(res.Bytes))
	case res.Bytes <= limit:
		fmt.Fprintf(r.Stdout, "cache is %s, under its %s limit; nothing to prune\n", humanBytes(res.Bytes), humanBytes(limit))
	default:
		fmt.Fprintf(r.Stdout, "%s %d objects (%s) of %s; limit %s\n", verb, res.Removed, humanBytes(res.RemovedBytes), humanBytes(res.Bytes), humanBytes(limit))
		if left := res.Bytes - res.RemovedBytes; left > limit {
			fmt.Fprintf(r.Stdout, "still %s: the rest was used in the last %s or is pinned\n", humanBytes(left), cacheMinAge)
		}
	}
	return nil
}
