package workspace

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// StatCache caches file content hashes by (size, mtime, mode) in
// .gs/stat_cache.json so workspace scans only re-hash modified files.
type StatCache struct {
	Files map[string]StatCacheEntry `json:"files"`
	// writtenAt is the cache file's modification time, on the same clock as
	// the files'.
	writtenAt time.Time
}

// StatCacheEntry records one regular file's stat fingerprint and content hash.
type StatCacheEntry struct {
	Size        int64  `json:"size"`
	ModTimeNs   int64  `json:"mtime_ns"`
	Mode        uint32 `json:"mode"`
	ContentHash string `json:"content_hash"`
}

// StatCacheClockSlack: a scan records only files last modified this long
// before it started, so a file changed while it was being hashed is hashed
// again next time.
const StatCacheClockSlack = 10 * time.Millisecond

func StatCachePath(root string) string {
	return filepath.Join(root, ".gs", "stat_cache.json")
}

func ReadStatCache(root string) StatCache {
	empty := StatCache{Files: map[string]StatCacheEntry{}}
	p := StatCachePath(root)
	info, err := os.Stat(p)
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return empty
	}
	var cache StatCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.Files == nil {
		return empty
	}
	cache.writtenAt = info.ModTime()
	return cache
}

// Hash returns a file's recorded content hash when it can be trusted.
func (c StatCache) Hash(rel string, size int64, modTime time.Time, mode uint32) (string, bool) {
	entry, ok := c.Files[rel]
	if !ok || entry.Size != size || entry.ModTimeNs != modTime.UnixNano() || entry.Mode != mode || entry.ContentHash == "" {
		return "", false
	}
	if !modTime.Before(c.writtenAt) {
		return "", false
	}
	return entry.ContentHash, true
}

// WriteStatCache replaces the cache atomically. Failing to write it only costs
// hashing later.
func WriteStatCache(root string, files map[string]StatCacheEntry) {
	dir := filepath.Join(root, ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(StatCache{Files: files})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "stat_cache-*.tmp")
	if err != nil {
		return
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), StatCachePath(root)) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// StatCacheEntryFor describes a regular file as a scan sees it; ok is false
// for anything else.
func StatCacheEntryFor(info fs.FileInfo, contentHash string) (StatCacheEntry, bool) {
	if !info.Mode().IsRegular() {
		return StatCacheEntry{}, false
	}
	mode := uint32(0o100644)
	if info.Mode()&0o111 != 0 {
		mode = 0o100755
	}
	return StatCacheEntry{Size: info.Size(), ModTimeNs: info.ModTime().UnixNano(), Mode: mode, ContentHash: contentHash}, true
}
