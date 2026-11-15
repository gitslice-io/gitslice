package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// editSource is how a command finds a workspace's edits against a base
// snapshot. An eager workspace walks and hashes its directory tree
// (walkEditSource). A lazy workspace (design/30_lazy_workspaces.md) will read
// the index of its local layer instead, so the cost follows the number of
// edits rather than the size of the slice. Every command that looks for edits
// (status, diff, create, modify, switch, sync, agent capture) goes through
// snapshotEditsAgainstBaseWithStats, which asks the workspace's source.
type editSource interface {
	edits(ctx context.Context, base BaseSnapshot, hashConcurrency int) (workspaceEdits, error)
}

// workspaceEdits is what an edit source found.
type workspaceEdits struct {
	// edits are sorted by path. Upserts carry their content hash and mode,
	// and no blob id until they are uploaded; their content is in the client
	// object cache, where the upload reads it.
	edits []*corev1.FileEdit
	// files holds the workspace's version of every path an upsert names (a
	// walk returns every file it saw), so a command can read the new content.
	files        map[string]workingFile
	scan         workspaceScanStats
	diffDuration time.Duration
}

// editSourceFor returns the edit source for a workspace: a walk for an eager
// one, the local layer (through its mount daemon, or local state when
// unmounted) for a lazy one.
func (r Runner) editSourceFor(ws WorkspaceConfig) (editSource, error) {
	root, err := r.workspaceRoot()
	if err != nil {
		return nil, err
	}
	id := lazyWorkspaceID(root)
	if id == "" {
		return walkEditSource{r: r, ws: ws}, nil
	}
	rec, err := r.readMountRecord(id)
	if err != nil {
		return nil, err
	}
	return overlayEditSource{fs: r.newDaemonLayerView(rec, root, nil), ws: ws, mountRoot: root}, nil
}

// walkEditSource finds edits by hashing every file of an eager workspace (the
// stat cache skips the files whose size, time and mode have not changed) and
// comparing the result with the base.
type walkEditSource struct {
	r  Runner
	ws WorkspaceConfig
}

func (s walkEditSource) edits(ctx context.Context, base BaseSnapshot, hashConcurrency int) (workspaceEdits, error) {
	current, scanStats, err := s.r.scanWorkspaceFilesWithStats(ctx, s.ws, hashConcurrency, base.Files)
	found := workspaceEdits{files: current, scan: scanStats}
	if err != nil {
		return found, err
	}
	diffStarted := time.Now()
	for p, file := range current {
		baseFile, ok := base.Files[p]
		if ok && baseFile.ContentHash == file.ContentHash && sameWorkspaceMode(baseFile.Mode, file.Mode) {
			continue
		}
		found.edits = append(found.edits, &corev1.FileEdit{Op: "upsert", Path: p, ContentHash: file.ContentHash, Mode: file.Mode})
	}
	for p := range base.Files {
		if _, ok := current[p]; !ok {
			found.edits = append(found.edits, &corev1.FileEdit{Op: "delete", Path: p})
		}
	}
	sortFileEdits(found.edits)
	found.diffDuration = time.Since(diffStarted)
	return found, nil
}

// readWorkingFile returns a workspace file's content: from the file on disk
// when there is one (confined without following symlinks), else from the client
// cache by its hash (a lazy workspace's file that was renamed but never read
// has no local copy).
func readWorkingFile(file workingFile, cache *clientcache.ObjectCache) ([]byte, error) {
	if file.AbsPath != "" {
		rootDir, relPath := splitConfinedWorkingPath(file)
		if isSymlinkMode(file.Mode) {
			if dir := filepath.Dir(relPath); dir != "." {
				root, err := os.OpenRoot(rootDir)
				if err != nil {
					return nil, err
				}
				_, statErr := root.Lstat(relPath)
				_ = root.Close()
				if statErr != nil {
					return nil, statErr
				}
			}
			target, err := os.Readlink(file.AbsPath)
			if err != nil {
				return nil, err
			}
			return []byte(target), nil
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && file.ContentHash != "" && cache != nil && cache.Exists(file.ContentHash) {
				return cache.Read(file.ContentHash)
			}
			return nil, err
		}
		f, err := root.Open(relPath)
		if err != nil {
			_ = root.Close()
			if errors.Is(err, fs.ErrNotExist) && file.ContentHash != "" && cache != nil && cache.Exists(file.ContentHash) {
				return cache.Read(file.ContentHash)
			}
			return nil, err
		}
		info, sErr := f.Stat()
		if sErr != nil {
			_ = f.Close()
			_ = root.Close()
			return nil, sErr
		}
		if !info.Mode().IsRegular() {
			_ = f.Close()
			_ = root.Close()
			return nil, fmt.Errorf("working file %s is not a regular file", file.AbsPath)
		}
		data, rErr := io.ReadAll(f)
		_ = f.Close()
		_ = root.Close()
		return data, rErr
	}
	return cache.Read(file.ContentHash)
}

func splitConfinedWorkingPath(file workingFile) (string, string) {
	if file.RelPath != "" {
		if cleanRel, err := cleanConfinedRelPath(file.RelPath); err == nil {
			relSlash := filepath.FromSlash(cleanRel)
			sepSuffix := string(filepath.Separator) + relSlash
			if strings.HasSuffix(file.AbsPath, sepSuffix) {
				rootDir := strings.TrimSuffix(file.AbsPath, sepSuffix)
				if rootDir == "" {
					rootDir = string(filepath.Separator)
				}
				return rootDir, relSlash
			}
		}
	}
	return filepath.Dir(file.AbsPath), filepath.Base(file.AbsPath)
}
