package cli

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gitslice.io/gitslice/internal/lazyfs"
)

// A lazy workspace's .gs lives in its state directory (served at <mount>/.gs),
// with the files gs init writes plus lazy.json, which names the workspace so
// commands know to ask its mount for edits instead of walking the tree.

type lazyMarker struct {
	ID string `json:"id"`
}

func (r Runner) mountGsDir(id string) string { return filepath.Join(r.mountStateDir(id), "gs") }

// writeLazyWorkspaceState writes the workspace files a new lazy workspace
// starts with, as gs init would: its slice, its state and its base snapshot.
func (r Runner) writeLazyWorkspaceState(rec mountRecord, manifest *lazyfs.Manifest) error {
	dir := r.mountGsDir(rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "slice.json")); err == nil {
		return nil // already there: the workspace's own state wins
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
		"slice.json":         ws, // last: its presence means the rest is there
	} {
		if err := writeJSONFile(filepath.Join(dir, name), value, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// lazyWorkspaceID returns the id of the lazy workspace rooted at root, or "".
func lazyWorkspaceID(root string) string {
	var marker lazyMarker
	if err := readJSONFile(filepath.Join(root, ".gs", "lazy.json"), &marker); err != nil {
		return ""
	}
	if !isValidMountID(marker.ID) {
		return ""
	}
	return marker.ID
}

// refuseInLazyWorkspace stops a command that has no meaning in a lazy
// workspace, saying what to run instead.
func (r Runner) refuseInLazyWorkspace(what, instead string) error {
	root, err := r.workspaceRoot()
	if err != nil || lazyWorkspaceID(root) == "" {
		return nil
	}
	return userError("lazy_workspace_unsupported", what+" does not apply to a lazy workspace (gs mount)", instead)
}

func multisetFileHash(p, contentHash string, mode uint32) (uint64, uint64) {
	var modeBuf [16]byte
	modeBytes := strconv.AppendUint(modeBuf[:0], uint64(mode), 8)
	h1 := fnv.New64a()
	_, _ = h1.Write([]byte(p))
	_, _ = h1.Write([]byte{0})
	_, _ = h1.Write([]byte(contentHash))
	_, _ = h1.Write([]byte{0})
	_, _ = h1.Write(modeBytes)
	s1 := h1.Sum64()

	h2 := fnv.New64()
	_, _ = h2.Write([]byte("gs-manifest-v1:"))
	_, _ = h2.Write(modeBytes)
	_, _ = h2.Write([]byte{0})
	_, _ = h2.Write([]byte(p))
	_, _ = h2.Write([]byte{0})
	_, _ = h2.Write([]byte(contentHash))
	s2 := h2.Sum64()
	return s1, s2
}

// manifestDigest computes an order-independent multiset digest of manifest's
// files, matching snapshotDigest(manifestSnapshot(manifest)).
func manifestDigest(manifest *lazyfs.Manifest) string {
	if manifest == nil {
		return ""
	}
	var count int
	var sum1, sum2, xor1 uint64
	manifest.Files(func(entry lazyfs.BaseEntry) {
		count++
		s1, s2 := multisetFileHash(entry.Path, entry.ContentHash, entry.Mode)
		sum1 += s1
		sum2 += s2
		xor1 ^= s1
	})
	return fmt.Sprintf("%d:%016x:%016x:%016x", count, sum1, sum2, xor1)
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

// daemonLayerView reads a mounted lazy workspace's layers through its daemon,
// which owns them: the edits and touched base entries over the control socket,
// files through the mount. When unmounted, it falls back to reading the local
// layer and manifest on disk if present.
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
	return reply.Edits, reply.BaseCommit, reply.BaseFiles, reply.BaseFilesDigest, true, nil
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
	layer, err := lazyfs.Open(local, manifest, cacheContent{cache: cache}, lazyfs.Options{NoSync: true})
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
			if edit.Op == "delete" || isSymlinkMode(edit.Mode) {
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
