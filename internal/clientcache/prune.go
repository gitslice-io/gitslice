package clientcache

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The cache is content addressed and every object in it can be fetched
// again, except content only this machine has: a lazy workspace's
// checkpoints and the local side of a sync conflict. Prune removes the least
// recently used objects once the cache grows past a limit, keeping what the
// caller pins and anything used recently (design/30_lazy_workspaces.md,
// "The client cache").

// touchInterval is how stale an object's modification time may get before a
// use refreshes it. The time marks the last use, for Prune; refreshing it at
// most this often keeps reads from writing to the disk each time.
const touchInterval = time.Hour

// touch marks an object as used now when its last mark is older than
// touchInterval.
func touch(path string, info os.FileInfo) {
	now := time.Now()
	if now.Sub(info.ModTime()) < touchInterval {
		return
	}
	_ = os.Chtimes(path, now, now)
}

// PruneOptions says how far Prune goes.
type PruneOptions struct {
	// MaxBytes is the size the cache may reach; Prune does nothing below it
	// and, above it, removes objects until the cache is at TargetBytes.
	MaxBytes int64
	// TargetBytes is where pruning stops; MaxBytes*4/5 when 0, so the next
	// few commands do not prune again.
	TargetBytes int64
	// MinAge keeps every object used more recently, whatever the size: a
	// command that checked an object is there and is about to read it keeps
	// it. At least twice touchInterval.
	MinAge time.Duration
	// Keep reports objects that must stay: content only this machine has.
	Keep func(contentHash string) bool
	// DryRun reports what would be removed without removing it.
	DryRun bool
}

// PruneResult is what the cache held and what Prune removed.
type PruneResult struct {
	Objects      int64 `json:"objects"`
	Bytes        int64 `json:"bytes"`
	Kept         int64 `json:"pinned"`
	Removed      int64 `json:"removed"`
	RemovedBytes int64 `json:"removed_bytes"`
}

type cachedObject struct {
	hash, path string
	size       int64
	used       time.Time
}

// Usage reports how many objects the cache holds and their size.
func (c *ObjectCache) Usage() (objects, bytes int64, err error) {
	all, err := c.objects()
	for _, object := range all {
		objects++
		bytes += object.size
	}
	return objects, bytes, err
}

// Prune removes least recently used objects while the cache is over
// opts.MaxBytes, down to opts.TargetBytes. Objects opts.Keep reports and
// objects used within opts.MinAge stay, even if that leaves the cache over
// the limit.
func (c *ObjectCache) Prune(opts PruneOptions) (PruneResult, error) {
	var result PruneResult
	all, err := c.objects()
	if err != nil {
		return result, err
	}
	for _, object := range all {
		result.Objects++
		result.Bytes += object.size
	}
	c.removeStaleTemp()
	if opts.MaxBytes <= 0 || result.Bytes <= opts.MaxBytes {
		return result, nil
	}
	target := opts.TargetBytes
	if target <= 0 || target > opts.MaxBytes {
		target = opts.MaxBytes / 5 * 4
	}
	minAge := max(opts.MinAge, 2*touchInterval)
	cutoff := time.Now().Add(-minAge)
	sort.Slice(all, func(i, j int) bool { return all[i].used.Before(all[j].used) })
	size := result.Bytes
	for _, object := range all {
		if size <= target {
			break
		}
		if !object.used.Before(cutoff) {
			break // sorted by last use: everything after is newer still
		}
		if opts.Keep != nil && opts.Keep(object.hash) {
			result.Kept++
			continue
		}
		if !opts.DryRun {
			// Read-only objects are removed through their writable directory.
			if err := os.Remove(object.path); err != nil && !os.IsNotExist(err) {
				continue
			}
		}
		size -= object.size
		result.Removed++
		result.RemovedBytes += object.size
	}
	return result, nil
}

// objects lists every object: objects/sha256/aa/bb/<digest>.
func (c *ObjectCache) objects() ([]cachedObject, error) {
	base := filepath.Join(c.root, "objects", "sha256")
	var out []cachedObject
	first, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, a := range first {
		if !a.IsDir() {
			continue
		}
		second, err := os.ReadDir(filepath.Join(base, a.Name()))
		if err != nil {
			continue
		}
		for _, b := range second {
			if !b.IsDir() {
				continue
			}
			dir := filepath.Join(base, a.Name(), b.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				name := f.Name()
				if f.IsDir() || len(name) != 64 || !strings.HasPrefix(name, a.Name()+b.Name()) {
					continue
				}
				info, err := f.Info()
				if err != nil {
					continue
				}
				out = append(out, cachedObject{hash: "sha256:" + name, path: filepath.Join(dir, name), size: info.Size(), used: info.ModTime()})
			}
		}
	}
	return out, nil
}

// removeStaleTemp deletes temporary files a crashed write left behind.
func (c *ObjectCache) removeStaleTemp() {
	dir := filepath.Join(c.root, "tmp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
