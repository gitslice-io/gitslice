package cli

import (
	"context"
	"os"
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
// one, the local layer (through its mount daemon) for a lazy one.
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
	manifest, err := r.loadMountManifest(rec)
	if err != nil {
		return nil, err
	}
	return overlayEditSource{fs: daemonLayerView{id: id, manifest: manifest, mountRoot: root}, ws: ws, mountRoot: root}, nil
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
// when there is one, else from the client cache by its hash (a lazy
// workspace's file that was renamed but never read has no local copy).
func readWorkingFile(file workingFile, cache *clientcache.ObjectCache) ([]byte, error) {
	if file.AbsPath != "" {
		return os.ReadFile(file.AbsPath)
	}
	return cache.Read(file.ContentHash)
}
