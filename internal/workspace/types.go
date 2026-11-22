package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	ErrNotInWorkspace   = errors.New("not in a gitslice workspace")
	ErrWorkspaceDamaged = errors.New("workspace has lost its slice binding")
	ErrReservedPath     = errors.New("path refers to reserved .gs metadata")
	ErrOutsideSlice     = errors.New("path is outside the workspace slice")
)

// DamagedError describes a .gs directory that is missing slice.json.
type DamagedError struct {
	GsDir string
}

func (e *DamagedError) Error() string {
	return fmt.Sprintf("found %s without slice.json, so this workspace has lost its slice binding", e.GsDir)
}

func (e *DamagedError) Unwrap() error { return ErrWorkspaceDamaged }

// ReservedPathError reports an attempt to access reserved .gs metadata.
type ReservedPathError struct {
	Path string
}

func (e *ReservedPathError) Error() string {
	return "path refers to reserved .gs metadata: " + e.Path
}

func (e *ReservedPathError) Unwrap() error { return ErrReservedPath }

// OutsideSliceError reports a path outside the workspace's bound slice.
type OutsideSliceError struct {
	Path string
}

func (e *OutsideSliceError) Error() string {
	return "path is outside the workspace slice: " + e.Path
}

func (e *OutsideSliceError) Unwrap() error { return ErrOutsideSlice }

// UserConfig is the CLI's persisted user configuration (~/.gitslice/config.json).
type UserConfig struct {
	ServerAddr string            `json:"server_addr"`
	Token      string            `json:"token"`
	SubjectID  string            `json:"subject_id"`
	Aliases    map[string]string `json:"aliases,omitempty"`
	// ActiveAccount is the account commands default to (gs account use), when
	// the user still belongs to it; otherwise their personal account.
	ActiveAccount string `json:"active_account,omitempty"`
}

// Config is the slice binding persisted at .gs/slice.json.
type Config struct {
	Account        string   `json:"account"`
	Slice          string   `json:"slice"`
	SliceID        string   `json:"slice_id"`
	DefinitionHash string   `json:"definition_hash"`
	IncludedPaths  []string `json:"included_paths"`
	BaseCommitID   string   `json:"base_commit_id"`
	// ConversationID links changesets authored in this workspace to an agent
	// conversation (set when an agent daemon hydrates a conversation workspace).
	ConversationID string `json:"conversation_id,omitempty"`
}

// State is the mutable workspace state persisted at .gs/state.json.
type State struct {
	CurrentChangesetID string `json:"current_changeset_id"`
	CurrentPatchsetID  string `json:"current_patchset_id"`
	BaseCommitID       string `json:"base_commit_id"`
	ActiveStackID      string `json:"active_stack_id,omitempty"`
}

// ConflictState is persisted at .gs/conflicts.json while sync/restack conflicts
// remain unresolved.
type ConflictState struct {
	OldBaseCommitID string     `json:"old_base_commit_id"`
	NewBaseCommitID string     `json:"new_base_commit_id"`
	ChangesetID     string     `json:"changeset_id,omitempty"`
	PatchsetID      string     `json:"patchset_id,omitempty"`
	Conflicts       []Conflict `json:"conflicts"`
}

// Conflict describes a single file conflict recorded in ConflictState.
type Conflict struct {
	Path              string `json:"path"`
	ConflictClass     string `json:"conflict_class"`
	OldBaseCommitID   string `json:"old_base_commit_id"`
	NewBaseCommitID   string `json:"new_base_commit_id"`
	BaseFingerprint   string `json:"base_fingerprint,omitempty"`
	LocalFingerprint  string `json:"local_fingerprint,omitempty"`
	RemoteFingerprint string `json:"remote_fingerprint,omitempty"`
	BaseContentHash   string `json:"base_content_hash,omitempty"`
	LocalContentHash  string `json:"local_content_hash,omitempty"`
	RemoteContentHash string `json:"remote_content_hash,omitempty"`
}

// BaseSnapshot is the persisted .gs/base_snapshot.json file table.
type BaseSnapshot struct {
	CommitID string                      `json:"commit_id"`
	Files    map[string]BaseSnapshotFile `json:"files"`
}

// BaseSnapshotFile describes one tracked file in BaseSnapshot.
type BaseSnapshotFile struct {
	Path        string `json:"path"`
	RelPath     string `json:"rel_path"`
	ContentHash string `json:"content_hash"`
	Mode        uint32 `json:"mode"`
	Size        int64  `json:"size"`
}

// FindRoot walks upward from dir to find the enclosing gitslice workspace root.
func FindRoot(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".gs", "slice.json")); err == nil {
			return dir, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// A .gs without its slice.json is a workspace that lost its binding.
		// Walking past it would make the next gs init or gs sync start a second
		// workspace inside it.
		if info, err := os.Stat(filepath.Join(dir, ".gs")); err == nil && info.IsDir() {
			return "", &DamagedError{GsDir: filepath.Join(dir, ".gs")}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotInWorkspace
		}
		dir = parent
	}
}

func ReadJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func WriteJSONFile(path string, v any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return WriteAtomicFile(path, data, mode)
}

func WriteCompactJSONFile(path string, v any, mode fs.FileMode) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return WriteAtomicFile(path, data, mode)
}

func WriteAtomicFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	dirMode := fs.FileMode(0o755)
	if mode&0o077 == 0 {
		dirMode = 0o700
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
