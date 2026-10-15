package cli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Workspace scans read only the files that changed since they were last
// hashed, as Git's index does (design/28_benchmarks.md). .gs/stat_cache.json
// records each file's size, modification time and mode with its content hash.
// A recorded hash is used only when all three still match, the file was last
// modified before the record was written (a change within the same clock tick
// would not move the modification time), and the client cache still holds the
// content. The cache is only an optimization: when it is missing or
// unreadable, every file is hashed.
type statCache struct {
	Files map[string]statCacheEntry `json:"files"`
	// writtenAt is the cache file's modification time, on the same clock as
	// the files'.
	writtenAt time.Time
}

type statCacheEntry struct {
	Size        int64  `json:"size"`
	ModTimeNs   int64  `json:"mtime_ns"`
	Mode        uint32 `json:"mode"`
	ContentHash string `json:"content_hash"`
}

// statCacheClockSlack: a scan records only files last modified this long
// before it started, so a file changed while it was being hashed is hashed
// again next time.
const statCacheClockSlack = 10 * time.Millisecond

func statCachePath(root string) string {
	return filepath.Join(root, ".gs", "stat_cache.json")
}

func readStatCache(root string) statCache {
	empty := statCache{Files: map[string]statCacheEntry{}}
	p := statCachePath(root)
	info, err := os.Stat(p)
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return empty
	}
	var cache statCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.Files == nil {
		return empty
	}
	cache.writtenAt = info.ModTime()
	return cache
}

// hash returns a file's recorded content hash when it can be trusted.
func (c statCache) hash(rel string, size int64, modTime time.Time, mode uint32) (string, bool) {
	entry, ok := c.Files[rel]
	if !ok || entry.Size != size || entry.ModTimeNs != modTime.UnixNano() || entry.Mode != mode || entry.ContentHash == "" {
		return "", false
	}
	if !modTime.Before(c.writtenAt) {
		return "", false
	}
	return entry.ContentHash, true
}

// writeStatCache replaces the cache, through a temporary file so a reader
// never sees half of it. Failing to write it only costs hashing later.
func writeStatCache(root string, files map[string]statCacheEntry) {
	dir := filepath.Join(root, ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(statCache{Files: files})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "stat_cache-*.tmp")
	if err != nil {
		return
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), statCachePath(root)) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// statCacheEntryFor describes a regular file as a scan sees it; ok is false
// for anything else.
func statCacheEntryFor(info fs.FileInfo, contentHash string) (statCacheEntry, bool) {
	if !info.Mode().IsRegular() {
		return statCacheEntry{}, false
	}
	mode := uint32(0o100644)
	if info.Mode()&0o111 != 0 {
		mode = 0o100755
	}
	return statCacheEntry{Size: info.Size(), ModTimeNs: info.ModTime().UnixNano(), Mode: mode, ContentHash: contentHash}, true
}
