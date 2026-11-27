package cli

import (
	"context"
	"errors"
	"fmt"
	"gitslice.io/gitslice/internal/workspace"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// overlayEditSource finds a lazy workspace's edits in its local layer
// (design/30_lazy_workspaces.md) instead of walking a directory. It compares
// only the paths the local layer touches, plus, when the base asked about is
// not the layer's own (a changeset lower in a dependency tree), the paths
// where the two bases differ. Untracked files that .gitignore covers are left
// out, as a walk leaves them out.
type overlayEditSource struct {
	fs layerView
	ws WorkspaceConfig
	// mountRoot, when the layers are mounted, is where commands read a
	// changed file's content (through the mount, which fetches what a moved
	// base file needs); otherwise the local layer's file, or the cache.
	mountRoot string
	// useLayerBase asks for edits directly against the layer's own base
	// without materializing a full BaseSnapshot on the client.
	useLayerBase bool
}

// layerView is what an overlay edit source needs of a lazy workspace's
// layers: a *lazyfs.FS, or a mounted workspace's daemon (*daemonLayerView).
type layerView interface {
	Edits() ([]lazyfs.Edit, error)
	Base() *lazyfs.Manifest
	ReadFile(ctx context.Context, p string) ([]byte, error)
}

type baseLookupView interface {
	EditsWithBase() (edits []lazyfs.Edit, baseCommit string, baseFiles map[string]BaseSnapshotFile, baseDigest string, liveMount bool, err error)
}

type overlayState struct {
	exists    bool
	hash      string
	mode      uint32
	localPath string
}

func (s overlayEditSource) edits(ctx context.Context, base BaseSnapshot, _ int) (workspaceEdits, error) {
	started := time.Now()
	var found workspaceEdits
	var (
		layerEdits []lazyfs.Edit
		baseCommit string
		baseFiles  map[string]BaseSnapshotFile
		baseDigest string
		liveMount  = s.mountRoot != ""
		err        error
	)
	if view, ok := s.fs.(baseLookupView); ok {
		layerEdits, baseCommit, baseFiles, baseDigest, liveMount, err = view.EditsWithBase()
	} else {
		layerEdits, err = s.fs.Edits()
	}
	if err != nil {
		return found, err
	}

	states := map[string]overlayState{}
	for _, edit := range layerEdits {
		if err := ensureNotReservedGsPath(edit.Path); err != nil {
			return found, err
		}
		if edit.Op == "delete" {
			states[edit.Path] = overlayState{}
			continue
		}
		states[edit.Path] = overlayState{exists: true, hash: edit.ContentHash, mode: edit.Mode, localPath: edit.LocalPath}
	}

	var manifest *lazyfs.Manifest
	useDaemonBase := false
	if liveMount {
		if s.useLayerBase {
			useDaemonBase = true
		} else if baseCommit != "" && baseDigest != "" && base.CommitID == baseCommit && snapshotDigest(base) == baseDigest {
			useDaemonBase = true
		}
	}
	if !useDaemonBase {
		manifest = s.fs.Base()
		if manifest == nil {
			return found, fmt.Errorf("lazy workspace base manifest is unavailable")
		}
		if s.useLayerBase {
			baseFiles = make(map[string]BaseSnapshotFile, len(states))
			for p := range states {
				if entry, ok := manifest.Lookup(p); ok && entry.Kind != lazyfs.KindDir {
					baseFiles[p] = BaseSnapshotFile{Path: entry.Path, ContentHash: entry.ContentHash, Mode: entry.Mode, Size: entry.Size}
				}
			}
		} else if base.CommitID != manifest.CommitID() || !sameFiles(base, manifest) {
			// Where the base asked about differs from the layer's base, the
			// workspace shows the layer's base unless the layer changed the path.
			for p, file := range base.Files {
				if _, ok := states[p]; ok {
					continue
				}
				entry, ok := manifest.Lookup(p)
				switch {
				case !ok || entry.Kind == lazyfs.KindDir:
					states[p] = overlayState{}
				case entry.ContentHash != file.ContentHash || !sameWorkspaceMode(file.Mode, entry.Mode):
					states[p] = overlayState{exists: true, hash: entry.ContentHash, mode: entry.Mode}
				}
			}
			manifest.Files(func(entry lazyfs.BaseEntry) {
				if _, ok := states[entry.Path]; ok {
					return
				}
				if _, ok := base.Files[entry.Path]; !ok {
					states[entry.Path] = overlayState{exists: true, hash: entry.ContentHash, mode: entry.Mode}
				}
			})
		}
	}

	lookupBase := func(p string) (BaseSnapshotFile, bool) {
		if s.useLayerBase {
			f, ok := baseFiles[p]
			return f, ok
		}
		if f, ok := base.Files[p]; ok {
			return f, true
		}
		if useDaemonBase && baseFiles != nil {
			f, ok := baseFiles[p]
			return f, ok
		}
		return BaseSnapshotFile{}, false
	}

	paths := make([]string, 0, len(states))
	for p := range states {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	found.files = map[string]workingFile{}
	var ignore *workspace.Ignore
	for _, p := range paths {
		state := states[p]
		baseFile, tracked := lookupBase(p)
		rel, err := workspaceRelPath(s.ws, p)
		if err != nil {
			return found, err
		}
		if state.exists && !tracked {
			if shouldSkip(rel, nil) {
				continue
			}
			if ignore == nil {
				ignore = s.ignoreRules(ctx, base, baseFiles, manifest)
			}
			if ignore.Skip(rel, false) {
				continue
			}
		}
		switch {
		case !state.exists && tracked:
			found.edits = append(found.edits, &corev1.FileEdit{Op: "delete", Path: p})
		case state.exists && (!tracked || baseFile.ContentHash != state.hash || !sameWorkspaceMode(baseFile.Mode, state.mode)):
			found.edits = append(found.edits, &corev1.FileEdit{Op: "upsert", Path: p, ContentHash: state.hash, Mode: state.mode})
			absPath := state.localPath
			if workspace.IsSymlinkMode(state.mode) {
				absPath = ""
			} else if liveMount && s.mountRoot != "" {
				absPath = filepath.Join(s.mountRoot, filepath.FromSlash(rel))
			}
			found.files[p] = workingFile{
				BaseSnapshotFile: BaseSnapshotFile{Path: p, RelPath: rel, ContentHash: state.hash, Mode: state.mode},
				AbsPath:          absPath,
			}
		}
	}
	sortFileEdits(found.edits)
	found.diffDuration = time.Since(started)
	return found, nil
}

// ignoreRules builds the .gitignore matcher over the workspace's layers. As
// in a walk, every file of the layer's base and of the base asked about
// counts as tracked.
func (s overlayEditSource) ignoreRules(ctx context.Context, base BaseSnapshot, baseFiles map[string]BaseSnapshotFile, manifest *lazyfs.Manifest) *workspace.Ignore {
	var ig *workspace.Ignore
	if manifest != nil {
		tracked := map[string]BaseSnapshotFile{}
		manifest.Files(func(entry lazyfs.BaseEntry) {
			tracked[entry.Path] = BaseSnapshotFile{Path: entry.Path}
		})
		ig = workspace.NewIgnore("", s.ws, tracked, base.Files)
	} else {
		ig = workspace.NewIgnore("", s.ws, baseFiles, base.Files)
	}
	ig.Read = func(dir string) []byte {
		globalPath, err := workspaceRelativePathToGlobalPath(s.ws, path.Join(dir, ".gitignore"))
		if err != nil {
			return nil
		}
		data, err := s.fs.ReadFile(ctx, globalPath)
		if err != nil {
			return nil
		}
		return data
	}
	return ig
}

// sameFiles reports whether base lists exactly the manifest's files.
func sameFiles(base BaseSnapshot, manifest *lazyfs.Manifest) bool {
	count := 0
	same := true
	manifest.Files(func(entry lazyfs.BaseEntry) {
		count++
		file, ok := base.Files[entry.Path]
		if !ok || file.ContentHash != entry.ContentHash || !sameWorkspaceMode(file.Mode, entry.Mode) {
			same = false
		}
	})
	return same && count == len(base.Files)
}

// cacheContent is lazyfs.Content over the client object cache: base content is
// read from the cache, and local files are hashed into it, where an upload
// finds them. Fetching what the cache lacks comes with gs mount (phase 2).
type cacheContent struct {
	cache *clientcache.ObjectCache
}

func (c cacheContent) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	file, err := c.cache.Open(hash)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("content %s is not in the client cache: %w", hash, err)
	}
	return file, err
}

func (c cacheContent) HashFile(p string) (string, error) {
	object, err := c.cache.PutFile(p)
	return object.ContentHash, err
}

func (c cacheContent) HashBytes(data []byte) (string, error) {
	object, err := c.cache.PutBytes(data)
	return object.ContentHash, err
}
