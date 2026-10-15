package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
)

type workingFile struct {
	BaseSnapshotFile
	AbsPath string
}

type workspaceScanStats struct {
	FileCount    int
	ByteCount    int64
	HashWorkers  int
	WalkDuration time.Duration
	HashDuration time.Duration
}

type workspaceScanCandidate struct {
	absPath    string
	globalPath string
	relPath    string
	mode       uint32
	size       int64
	modTime    time.Time
	linkTarget *string // set for symlinks; hashed instead of the file they point to
}

// recordStatCache records the scanned files' stats and hashes, leaving out
// files modified too close to the scan to be trusted later. It writes the
// cache only when something changed.
func recordStatCache(root string, known statCache, candidates []workspaceScanCandidate, objects []clientcache.Object, scanStarted time.Time) {
	recordBefore := scanStarted.Add(-statCacheClockSlack)
	recorded := make(map[string]statCacheEntry, len(candidates))
	changed := false
	for i, candidate := range candidates {
		if candidate.linkTarget != nil || !candidate.modTime.Before(recordBefore) {
			continue
		}
		entry := statCacheEntry{Size: candidate.size, ModTimeNs: candidate.modTime.UnixNano(), Mode: candidate.mode, ContentHash: objects[i].ContentHash}
		if old, ok := known.Files[candidate.relPath]; !ok || old != entry {
			changed = true
		}
		recorded[candidate.relPath] = entry
	}
	if changed || len(recorded) != len(known.Files) {
		writeStatCache(root, recorded)
	}
}

// scanWorkspaceFiles hashes the workspace's files. Untracked paths matched by
// .gitignore files are skipped; tracked adds snapshots whose files count as
// tracked besides the workspace base.
func (r Runner) scanWorkspaceFiles(ws WorkspaceConfig, tracked ...map[string]BaseSnapshotFile) (map[string]workingFile, error) {
	files, _, err := r.scanWorkspaceFilesWithStats(context.Background(), ws, defaultCaptureHashConcurrency(), tracked...)
	return files, err
}

func (r Runner) scanWorkspaceFilesWithStats(ctx context.Context, ws WorkspaceConfig, concurrency int, tracked ...map[string]BaseSnapshotFile) (map[string]workingFile, workspaceScanStats, error) {
	var stats workspaceScanStats
	cache, err := r.objectCache()
	if err != nil {
		return nil, stats, err
	}
	root, err := r.workspaceRoot()
	if err != nil {
		return nil, stats, err
	}
	if len(ws.IncludedPaths) == 0 {
		return nil, stats, fmt.Errorf("workspace has no included paths")
	}
	base, err := r.readBaseSnapshot()
	if err != nil {
		return nil, stats, err
	}
	ignore := newWorkspaceIgnore(root, ws, append([]map[string]BaseSnapshotFile{base.Files}, tracked...)...)
	candidates := []workspaceScanCandidate{}
	walkStarted := time.Now()
	known := readStatCache(root)
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldSkip(rel, entry) || ignore.skip(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		globalPath, err := workspaceRelativePathToGlobalPath(ws, rel)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			candidates = append(candidates, workspaceScanCandidate{
				absPath:    p,
				globalPath: globalPath,
				relPath:    rel,
				mode:       0o120000,
				size:       int64(len(link)),
				linkTarget: &link,
			})
			stats.ByteCount += int64(len(link))
			return nil
		}
		mode := uint32(0o100644)
		if info.Mode()&0o111 != 0 {
			mode = 0o100755
		}
		candidates = append(candidates, workspaceScanCandidate{
			absPath:    p,
			globalPath: globalPath,
			relPath:    rel,
			mode:       mode,
			size:       info.Size(),
			modTime:    info.ModTime(),
		})
		stats.ByteCount += info.Size()
		return nil
	})
	stats.WalkDuration = time.Since(walkStarted)
	stats.FileCount = len(candidates)
	if err != nil {
		return nil, stats, err
	}

	// Files whose recorded stat still matches are not read again.
	objects := make([]clientcache.Object, len(candidates))
	var toHash []int
	for i, candidate := range candidates {
		if candidate.linkTarget == nil {
			if hash, ok := known.hash(candidate.relPath, candidate.size, candidate.modTime, candidate.mode); ok && cache.Exists(hash) {
				objects[i] = clientcache.Object{ContentHash: hash, Size: candidate.size}
				continue
			}
		}
		toHash = append(toHash, i)
	}
	paths := make([]string, len(toHash))
	links := map[string]string{}
	for j, i := range toHash {
		paths[j] = candidates[i].absPath
		if candidates[i].linkTarget != nil {
			links[candidates[i].absPath] = *candidates[i].linkTarget
		}
	}
	hashFile := cache.PutFile
	if len(links) > 0 {
		hashFile = func(path string) (clientcache.Object, error) {
			if link, ok := links[path]; ok {
				return cache.PutBytes([]byte(link))
			}
			return cache.PutFile(path)
		}
	}
	stats.HashWorkers = boundedCaptureHashConcurrency(concurrency, len(paths))
	hashStarted := time.Now()
	hashed, err := hashWorkspaceFiles(ctx, paths, stats.HashWorkers, hashFile)
	stats.HashDuration = time.Since(hashStarted)
	if err != nil {
		return nil, stats, err
	}
	for j, i := range toHash {
		objects[i] = hashed[j]
	}
	recordStatCache(root, known, candidates, objects, walkStarted)

	files := make(map[string]workingFile, len(candidates))
	for i, candidate := range candidates {
		cached := objects[i]
		files[candidate.globalPath] = workingFile{
			BaseSnapshotFile: BaseSnapshotFile{
				Path:        candidate.globalPath,
				RelPath:     candidate.relPath,
				ContentHash: cached.ContentHash,
				Mode:        candidate.mode,
				Size:        candidate.size,
			},
			AbsPath: candidate.absPath,
		}
	}
	return files, stats, nil
}

func hashWorkspaceFiles(ctx context.Context, paths []string, concurrency int, hashFile func(string) (clientcache.Object, error)) ([]clientcache.Object, error) {
	if len(paths) == 0 {
		return []clientcache.Object{}, nil
	}
	concurrency = boundedCaptureHashConcurrency(concurrency, len(paths))
	type result struct {
		index  int
		object clientcache.Object
		err    error
	}
	jobs := make(chan int)
	results := make(chan result, len(paths))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if err := ctx.Err(); err != nil {
					results <- result{index: index, err: err}
					continue
				}
				object, err := hashFile(paths[index])
				if err != nil {
					err = fmt.Errorf("hash workspace file %s: %w", paths[index], err)
				}
				results <- result{index: index, object: object, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range paths {
			select {
			case <-ctx.Done():
				return
			case jobs <- index:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	objects := make([]clientcache.Object, len(paths))
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		objects[result.index] = result.object
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}

func shouldSkip(rel string, _ fs.DirEntry) bool {
	if !strings.Contains(rel, "/") && (rel == ".agent-meta.json" || strings.HasPrefix(rel, ".agent-meta-") && strings.HasSuffix(rel, ".tmp")) {
		return true
	}
	first := rel
	if idx := strings.Index(first, "/"); idx >= 0 {
		first = first[:idx]
	}
	switch first {
	case ".git", ".gs", ".gitslice":
		return true
	default:
		return false
	}
}
