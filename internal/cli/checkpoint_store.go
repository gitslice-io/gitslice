package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
)

// Checkpoints (design/30_lazy_workspaces.md, "Checkpoints") are a lazy
// workspace's edits saved on this machine: what gs status would report, by
// content hash, with the content in the local object cache. Nothing is
// uploaded. gs takes one before anything replaces the local layer (sync,
// switch, restore, rewind), and gs checkpoint takes one on demand. gs rewind
// puts the workspace back to one; gs fork mounts a new workspace from one.

// maxAutoCheckpoints is how many automatic checkpoints a workspace keeps;
// ones taken with gs checkpoint are kept until deleted with the workspace.
const maxAutoCheckpoints = 50

type checkpointRecord struct {
	Number     int              `json:"number"`
	CreatedAt  string           `json:"created_at"`
	Message    string           `json:"message,omitempty"`
	Auto       bool             `json:"auto,omitempty"`
	BaseCommit string           `json:"base_commit"`
	Edits      []checkpointEdit `json:"edits"`
}

type checkpointEdit struct {
	Op          string `json:"op"` // upsert or delete
	Path        string `json:"path"`
	ContentHash string `json:"content_hash,omitempty"`
	Mode        uint32 `json:"mode,omitempty"`
}

func (r Runner) checkpointDir(id string) string {
	return filepath.Join(r.mountStateDir(id), "checkpoints")
}

func (r Runner) checkpoints(id string) ([]checkpointRecord, error) {
	entries, err := os.ReadDir(r.checkpointDir(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []checkpointRecord
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var cp checkpointRecord
		if err := readJSONFile(filepath.Join(r.checkpointDir(id), entry.Name()), &cp); err != nil {
			continue
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// takeCheckpoint saves the workspace's edits. An automatic checkpoint that
// would repeat the newest one is not taken.
func (r Runner) takeCheckpoint(ctx context.Context, lw *lazyWorkspace, ws WorkspaceConfig, message string, auto bool) (checkpointRecord, bool, error) {
	baseCommit := lw.baseCommit()
	source := overlayEditSource{fs: lw.layerView(), ws: ws, mountRoot: lw.root, useLayerBase: true}
	reported, err := source.edits(ctx, BaseSnapshot{CommitID: baseCommit}, 0)
	if err != nil {
		return checkpointRecord{}, false, err
	}
	cache, err := r.objectCache()
	if err != nil {
		return checkpointRecord{}, false, err
	}
	var mountRoot *os.Root
	defer func() {
		if mountRoot != nil {
			_ = mountRoot.Close()
		}
	}()
	edits := make([]checkpointEdit, 0, len(reported.edits))
	for _, edit := range reported.edits {
		cp := checkpointEdit{Op: edit.GetOp(), Path: edit.GetPath(), ContentHash: edit.GetContentHash(), Mode: edit.GetMode()}
		if err := ensureNotReservedGsPath(cp.Path); err != nil {
			return checkpointRecord{}, false, err
		}
		if cp.Op != "delete" {
			cp.Op = "upsert"
			// The content must outlive the file: keep it in the cache.
			if !cache.Exists(cp.ContentHash) {
				rel, err := cleanConfinedRelPath(strings.TrimPrefix(cp.Path, "/"))
				if err != nil {
					return checkpointRecord{}, false, err
				}
				if cp.Mode == lazyfs.ModeSymlink {
					target, err := os.Readlink(filepath.Join(lw.root, filepath.FromSlash(rel)))
					if err != nil {
						return checkpointRecord{}, false, err
					}
					if _, err := cache.PutBytes([]byte(target)); err != nil {
						return checkpointRecord{}, false, err
					}
				} else {
					if mountRoot == nil {
						if mountRoot, err = os.OpenRoot(lw.root); err != nil {
							return checkpointRecord{}, false, err
						}
					}
					file, err := mountRoot.Open(filepath.FromSlash(rel))
					if err != nil {
						return checkpointRecord{}, false, err
					}
					info, sErr := file.Stat()
					if sErr != nil {
						_ = file.Close()
						return checkpointRecord{}, false, sErr
					}
					if !info.Mode().IsRegular() {
						_ = file.Close()
						return checkpointRecord{}, false, fmt.Errorf("checkpoint source %s is not a regular file", cp.Path)
					}
					_, pErr := cache.PutReader(file)
					_ = file.Close()
					if pErr != nil {
						return checkpointRecord{}, false, pErr
					}
				}
			}
		}
		edits = append(edits, cp)
	}
	existing, err := r.checkpoints(lw.id)
	if err != nil {
		return checkpointRecord{}, false, err
	}
	if auto {
		if len(edits) == 0 {
			return checkpointRecord{}, false, nil
		}
		if n := len(existing); n > 0 && existing[n-1].BaseCommit == baseCommit && sameCheckpointEdits(existing[n-1].Edits, edits) {
			return existing[n-1], false, nil
		}
	}
	number := 1
	if n := len(existing); n > 0 {
		number = existing[n-1].Number + 1
	}
	cp := checkpointRecord{
		Number:     number,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Message:    strings.TrimSpace(message),
		Auto:       auto,
		BaseCommit: baseCommit,
		Edits:      edits,
	}
	if err := os.MkdirAll(r.checkpointDir(lw.id), 0o700); err != nil {
		return cp, false, err
	}
	if err := writeJSONFile(filepath.Join(r.checkpointDir(lw.id), strconv.Itoa(number)+".json"), cp, 0o600); err != nil {
		return cp, false, err
	}
	r.pruneCheckpoints(lw.id, append(existing, cp))
	return cp, true, nil
}

// autoCheckpoint takes a checkpoint before reason replaces the layer. A
// failure is said, not fatal: it must not stop the command it protects.
func (r Runner) autoCheckpoint(ctx context.Context, lw *lazyWorkspace, reason string) {
	ws, err := r.readWorkspaceConfig()
	if err == nil {
		_, _, err = r.takeCheckpoint(ctx, lw, ws, reason, true)
	}
	if err != nil {
		fmt.Fprintf(r.stderr(), "warning: no checkpoint before %s: %v\n", reason, err)
	}
}

func (r Runner) pruneCheckpoints(id string, all []checkpointRecord) {
	var auto []checkpointRecord
	for _, cp := range all {
		if cp.Auto {
			auto = append(auto, cp)
		}
	}
	for len(auto) > maxAutoCheckpoints {
		_ = os.Remove(filepath.Join(r.checkpointDir(id), strconv.Itoa(auto[0].Number)+".json"))
		auto = auto[1:]
	}
}

func sameCheckpointEdits(a, b []checkpointEdit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkpointSnapshot is the workspace as the checkpoint saw it: base with
// the checkpoint's edits.
func checkpointSnapshot(base *lazyfs.Manifest, cp checkpointRecord) (BaseSnapshot, error) {
	snapshot := manifestSnapshot(base)
	for _, edit := range cp.Edits {
		if err := ensureNotReservedGsPath(edit.Path); err != nil {
			return BaseSnapshot{}, err
		}
		if edit.Op == "delete" {
			delete(snapshot.Files, edit.Path)
			continue
		}
		snapshot.Files[edit.Path] = BaseSnapshotFile{Path: edit.Path, RelPath: strings.TrimPrefix(edit.Path, "/"), ContentHash: edit.ContentHash, Mode: edit.Mode}
	}
	return snapshot, nil
}

func (r Runner) findCheckpoint(id, selector string) (checkpointRecord, error) {
	all, err := r.checkpoints(id)
	if err != nil {
		return checkpointRecord{}, err
	}
	if len(all) == 0 {
		return checkpointRecord{}, userError("no_checkpoints", "this workspace has no checkpoints", "gs checkpoint saves one; gs takes one before sync, switch, restore and rewind.")
	}
	number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(selector), "#"))
	if err != nil {
		return checkpointRecord{}, userError("invalid_checkpoint", "a checkpoint is named by its number: "+selector, "gs checkpoint list shows them.")
	}
	for _, cp := range all {
		if cp.Number == number {
			return cp, nil
		}
	}
	return checkpointRecord{}, userError("checkpoint_not_found", "no checkpoint "+selector+" in this workspace", "gs checkpoint list shows them.")
}
