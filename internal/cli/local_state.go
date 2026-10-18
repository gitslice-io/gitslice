package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type UserConfig struct {
	ServerAddr string            `json:"server_addr"`
	Token      string            `json:"token"`
	SubjectID  string            `json:"subject_id"`
	Aliases    map[string]string `json:"aliases,omitempty"`
	// ActiveAccount is the account commands default to (gs account use), when
	// the user still belongs to it; otherwise their personal account.
	ActiveAccount string `json:"active_account,omitempty"`
}

type WorkspaceConfig struct {
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

type WorkspaceState struct {
	CurrentChangesetID string `json:"current_changeset_id"`
	CurrentPatchsetID  string `json:"current_patchset_id"`
	BaseCommitID       string `json:"base_commit_id"`
	ActiveStackID      string `json:"active_stack_id,omitempty"`
}

type WorkspaceConflictState struct {
	OldBaseCommitID string              `json:"old_base_commit_id"`
	NewBaseCommitID string              `json:"new_base_commit_id"`
	ChangesetID     string              `json:"changeset_id,omitempty"`
	PatchsetID      string              `json:"patchset_id,omitempty"`
	Conflicts       []WorkspaceConflict `json:"conflicts"`
}

type WorkspaceConflict struct {
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

type BaseSnapshot struct {
	CommitID string                      `json:"commit_id"`
	Files    map[string]BaseSnapshotFile `json:"files"`
}

type BaseSnapshotFile struct {
	Path        string `json:"path"`
	RelPath     string `json:"rel_path"`
	ContentHash string `json:"content_hash"`
	Mode        uint32 `json:"mode"`
	Size        int64  `json:"size"`
}

func (r Runner) loadLocalState() (UserConfig, WorkspaceConfig, WorkspaceState, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		return UserConfig{}, WorkspaceConfig{}, WorkspaceState{}, err
	}
	return cfg, ws, state, nil
}

func rejectPreStackWorkspaceState(state WorkspaceState) error {
	if state.ActiveStackID == "" && (state.CurrentChangesetID != "" || state.CurrentPatchsetID != "") {
		return userError(
			"unsupported_workspace_state",
			"workspace metadata is from an unsupported pre-dependency format",
			"Run gs init in a new directory or use a one-time migration tool.",
		)
	}
	return nil
}

func (r Runner) readUserConfig() (UserConfig, error) {
	if _, err := os.Stat(r.userConfigPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return UserConfig{}, userError("not_logged_in", "not logged in", "Run gs auth login.")
		}
		return UserConfig{}, err
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return cfg, err
	}
	if cfg.ServerAddr == "" || cfg.Token == "" {
		return cfg, userError("invalid_user_config", "invalid user config", "Run gs auth login again.")
	}
	return cfg, nil
}

func (r Runner) readPartialUserConfig() (UserConfig, error) {
	var cfg UserConfig
	if err := readJSONFile(r.userConfigPath(), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	return cfg, nil
}

func (r Runner) writeUserConfig(cfg UserConfig) error {
	if err := os.MkdirAll(filepath.Dir(r.userConfigPath()), 0o700); err != nil {
		return err
	}
	return writeJSONFile(r.userConfigPath(), cfg, 0o600)
}

func (r Runner) readWorkspaceConfig() (WorkspaceConfig, error) {
	var cfg WorkspaceConfig
	root, err := r.workspaceRoot()
	if err != nil {
		return cfg, err
	}
	if err := readJSONFile(filepath.Join(root, ".gs", "slice.json"), &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, userError("not_in_workspace", "not in a gitslice workspace", "Run gs init <slice|account:slice>.")
		}
		return cfg, err
	}
	return cfg, nil
}

func (r Runner) writeWorkspaceConfig(cfg WorkspaceConfig) error {
	root, err := r.workspaceRoot()
	if err != nil {
		if !isUserErrorCode(err, "not_in_workspace") {
			return err
		}
		root = r.cwd()
	}
	dir := filepath.Join(root, ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "slice.json"), cfg, 0o644)
}

func (r Runner) readWorkspaceState() (WorkspaceState, error) {
	var state WorkspaceState
	root, err := r.workspaceRoot()
	if err != nil {
		return state, err
	}
	err = readJSONFile(filepath.Join(root, ".gs", "state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	return state, nil
}

func (r Runner) readBaseSnapshot() (BaseSnapshot, error) {
	var snapshot BaseSnapshot
	root, err := r.workspaceRoot()
	if err != nil {
		return snapshot, err
	}
	err = readJSONFile(filepath.Join(root, ".gs", "base_snapshot.json"), &snapshot)
	if errors.Is(err, os.ErrNotExist) {
		snapshot.Files = map[string]BaseSnapshotFile{}
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if snapshot.Files == nil {
		snapshot.Files = map[string]BaseSnapshotFile{}
	}
	return snapshot, nil
}

func (r Runner) writeBaseSnapshot(snapshot BaseSnapshot) error {
	if snapshot.Files == nil {
		snapshot.Files = map[string]BaseSnapshotFile{}
	}
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "base_snapshot.json"), snapshot, 0o644)
}

func (r Runner) writeWorkspaceState(state WorkspaceState) error {
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "state.json"), state, 0o644)
}

func (r Runner) readWorkspaceConflictState() (WorkspaceConflictState, bool, error) {
	var state WorkspaceConflictState
	root, err := r.workspaceRoot()
	if err != nil {
		return state, false, err
	}
	err = readJSONFile(filepath.Join(root, ".gs", "conflicts.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	return state, len(state.Conflicts) > 0, nil
}

func (r Runner) writeWorkspaceConflictState(state WorkspaceConflictState) error {
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".gs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, "conflicts.json"), state, 0o644)
}

func (r Runner) clearWorkspaceConflictState() error {
	root, err := r.workspaceRoot()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(root, ".gs", "conflicts.json"))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r Runner) userConfigPath() string {
	return filepath.Join(r.homeDir(), ".gitslice", "config.json")
}

func (r Runner) workspaceRoot() (string, error) {
	return workspaceRootFrom(r.cwd())
}

// workspaceRootFrom finds the gitslice workspace that contains dir, walking up
// from it.
func workspaceRootFrom(dir string) (string, error) {
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
			return "", userError("workspace_damaged", "found "+filepath.Join(dir, ".gs")+" without slice.json, so this workspace has lost its slice binding",
				"Restore .gs/slice.json, or move the files you want to keep out and run gs init <slice|account:slice> in a new empty directory.")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", userError("not_in_workspace", "not in a gitslice workspace", "Run gs init <slice|account:slice>.")
		}
		dir = parent
	}
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSONFile(path string, v any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, mode)
}
