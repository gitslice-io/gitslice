package cli

import (
	"context"
	"errors"
	"fmt"
	"gitslice.io/gitslice/internal/workspace"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/lazyfs"
	"gitslice.io/gitslice/internal/mountd"
)

type lazyMarker struct {
	ID string `json:"id"`
}

func (r Runner) writeLazyWorkspaceState(rec mountRecord, manifest *lazyfs.Manifest) error {
	dir := r.mountGsDir(rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "slice.json")); err == nil {
		return nil
	}
	ws := WorkspaceConfig{
		Account:        rec.Account,
		Slice:          rec.Slice,
		SliceID:        rec.SliceID,
		DefinitionHash: rec.DefinitionHash,
		IncludedPaths:  rec.IncludedPaths,
		BaseCommitID:   rec.BaseCommit,
		ConversationID: rec.ConversationID,
	}
	base := manifestSnapshot(manifest)
	for name, value := range map[string]any{
		"base_snapshot.json": base,
		"state.json":         WorkspaceState{BaseCommitID: rec.BaseCommit},
		"lazy.json":          lazyMarker{ID: rec.ID},
		"slice.json":         ws,
	} {
		if err := writeJSONFile(filepath.Join(dir, name), value, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) refuseInLazyWorkspace(what, instead string) error {
	root, err := r.workspaceRoot()
	if err != nil || lazyWorkspaceID(root) == "" {
		return nil
	}
	return userError("lazy_workspace_unsupported", what+" does not apply to a lazy workspace (gs mount)", instead)
}

func snapshotDigest(base BaseSnapshot) string {
	var count int
	var sum1, sum2, xor1 uint64
	for p, file := range base.Files {
		count++
		s1, s2 := multisetFileHash(p, file.ContentHash, file.Mode)
		sum1 += s1
		sum2 += s2
		xor1 ^= s1
	}
	return fmt.Sprintf("%d:%016x:%016x:%016x", count, sum1, sum2, xor1)
}

type daemonLayerView struct {
	r           Runner
	rec         mountRecord
	id          string
	manifest    *lazyfs.Manifest
	mountRoot   string
	offlineRead func(ctx context.Context, p string) ([]byte, error)
}

func (r Runner) newDaemonLayerView(rec mountRecord, mountRoot string, manifest *lazyfs.Manifest) *daemonLayerView {
	return &daemonLayerView{
		r:         r,
		rec:       rec,
		id:        rec.ID,
		manifest:  manifest,
		mountRoot: mountRoot,
	}
}

func (v *daemonLayerView) EditsWithBase() ([]lazyfs.Edit, string, map[string]BaseSnapshotFile, string, bool, error) {
	reply, err := v.r.callMountDaemon(v.id, mountControlRequest{Op: "edits"})
	if errors.Is(err, errMountUnreachable) {
		edits, m, oerr := v.offlineEdits()
		if oerr == nil {
			v.manifest = m
			return edits, m.CommitID(), nil, "", false, nil
		}
		return nil, "", nil, "", false, userError("lazy_workspace_not_mounted", "cannot reach the mount of this lazy workspace: "+err.Error(), "Mount it again: gs mount <slice> "+v.mountRoot+".")
	}
	if err != nil {
		return nil, "", nil, "", false, err
	}
	if v.manifest != nil && reply.BaseCommit != "" && reply.BaseCommit != v.manifest.CommitID() {
		return nil, "", nil, "", false, userError("workspace_changed", "the lazy workspace moved to another base meanwhile", "Run the command again.")
	}
	if reply.BaseCommit != "" {
		v.rec.BaseCommit = reply.BaseCommit
	}
	var baseFiles map[string]BaseSnapshotFile
	if len(reply.BaseFiles) > 0 {
		baseFiles = make(map[string]BaseSnapshotFile, len(reply.BaseFiles))
		for k, f := range reply.BaseFiles {
			baseFiles[k] = BaseSnapshotFile{
				Path:        f.Path,
				RelPath:     f.RelPath,
				ContentHash: f.ContentHash,
				Mode:        f.Mode,
				Size:        f.Size,
			}
		}
	}
	return reply.Edits, reply.BaseCommit, baseFiles, reply.BaseFilesDigest, true, nil
}

func (v *daemonLayerView) offlineEdits() ([]lazyfs.Edit, *lazyfs.Manifest, error) {
	if !isValidMountID(v.id) {
		return nil, nil, errInvalidMountID
	}
	local := filepath.Join(v.r.mountStateDir(v.id), "local")
	info, err := os.Lstat(local)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("local layer is not a real directory")
	}
	rec := v.rec
	if rec.ID == "" {
		rec, err = v.r.readMountRecord(v.id)
		if err != nil {
			return nil, nil, err
		}
	}
	if recorded, rErr := lazyfs.RecordedBase(local); rErr == nil && recorded != "" && recorded != rec.BaseCommit {
		if _, sErr := os.Stat(v.r.mountManifestPath(v.id, recorded)); sErr == nil {
			rec.BaseCommit = recorded
		}
	}
	manifest := v.manifest
	if manifest == nil || manifest.CommitID() != rec.BaseCommit {
		manifest, err = v.r.loadMountManifest(rec)
		if err != nil {
			return nil, nil, err
		}
	}
	cache, err := v.r.objectCache()
	if err != nil {
		return nil, nil, err
	}
	layer, err := lazyfs.Open(local, manifest, mountd.CacheContent{Cache: cache}, lazyfs.Options{NoSync: true})
	if err != nil {
		return nil, nil, err
	}
	defer layer.Close()
	edits, err := layer.Edits()
	if err != nil {
		return nil, nil, err
	}
	editByPath := make(map[string]lazyfs.Edit, len(edits))
	for _, edit := range edits {
		editByPath[edit.Path] = edit
	}
	v.offlineRead = func(ctx context.Context, p string) ([]byte, error) {
		if edit, ok := editByPath[p]; ok {
			if edit.Op == "delete" || workspace.IsSymlinkMode(edit.Mode) {
				return nil, fs.ErrNotExist
			}
			if edit.LocalPath != "" {
				return os.ReadFile(edit.LocalPath)
			}
			return cache.Read(edit.ContentHash)
		}
		entry, ok := manifest.Lookup(p)
		if !ok || entry.Kind != lazyfs.KindFile {
			return nil, fs.ErrNotExist
		}
		return cache.Read(entry.ContentHash)
	}
	return edits, manifest, nil
}

func (v *daemonLayerView) Edits() ([]lazyfs.Edit, error) {
	edits, _, _, _, _, err := v.EditsWithBase()
	return edits, err
}

func (v *daemonLayerView) Base() *lazyfs.Manifest {
	if v.manifest != nil {
		return v.manifest
	}
	rec := v.rec
	if rec.ID == "" {
		if loaded, err := v.r.readMountRecord(v.id); err == nil {
			rec = loaded
		}
	}
	if m, err := v.r.loadMountManifest(rec); err == nil {
		v.manifest = m
	}
	return v.manifest
}

func (v *daemonLayerView) ReadFile(ctx context.Context, p string) ([]byte, error) {
	if err := ensureNotReservedGsPath(p); err != nil {
		return nil, err
	}
	if v.offlineRead != nil {
		return v.offlineRead(ctx, p)
	}
	rel, err := cleanConfinedRelPath(strings.TrimPrefix(p, "/"))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(v.mountRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fs.ErrInvalid
	}
	return io.ReadAll(file)
}
