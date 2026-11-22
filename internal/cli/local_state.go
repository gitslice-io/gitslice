package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/workspace"
)

type (
	UserConfig             = workspace.UserConfig
	WorkspaceConfig        = workspace.Config
	WorkspaceState         = workspace.State
	WorkspaceConflictState = workspace.ConflictState
	WorkspaceConflict      = workspace.Conflict
	BaseSnapshot           = workspace.BaseSnapshot
	BaseSnapshotFile       = workspace.BaseSnapshotFile
)

// WorkspaceMetadataStore coordinates reads and atomic multi-file commits across
// .gs/{slice.json,state.json,base_snapshot.json,conflicts.json} so BaseCommitID
// never drifts across files if a sync or submit step fails midway.
type WorkspaceMetadataStore struct {
	r        Runner
	Config   WorkspaceConfig
	State    WorkspaceState
	Snapshot BaseSnapshot
}

func (r Runner) openWorkspaceMetadataStore() (*WorkspaceMetadataStore, error) {
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		return nil, err
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		return nil, err
	}
	snapshot, err := r.readBaseSnapshot()
	if err != nil {
		return nil, err
	}
	return &WorkspaceMetadataStore{
		r:        r,
		Config:   ws,
		State:    state,
		Snapshot: snapshot,
	}, nil
}

// CommitBase updates slice.json, state.json, and base_snapshot.json to the same
// BaseCommitID in one step, and optionally updates or clears conflicts.json.
func (m *WorkspaceMetadataStore) CommitBase(ws WorkspaceConfig, state WorkspaceState, snapshot BaseSnapshot, conflicts *WorkspaceConflictState) error {
	commitID := firstNonEmpty(snapshot.CommitID, state.BaseCommitID, ws.BaseCommitID)
	if commitID != "" {
		ws.BaseCommitID = commitID
		state.BaseCommitID = commitID
		snapshot.CommitID = commitID
	}
	if err := m.r.writeWorkspaceConfig(ws); err != nil {
		return err
	}
	if err := m.r.writeBaseSnapshot(snapshot); err != nil {
		return err
	}
	if err := m.r.writeWorkspaceState(state); err != nil {
		return err
	}
	if conflicts != nil {
		if len(conflicts.Conflicts) > 0 {
			if err := m.r.writeWorkspaceConflictState(*conflicts); err != nil {
				return err
			}
		} else if err := m.r.clearWorkspaceConflictState(); err != nil {
			return err
		}
	}
	m.Config = ws
	m.State = state
	m.Snapshot = snapshot
	return nil
}

func (r Runner) commitWorkspaceBase(ws WorkspaceConfig, state WorkspaceState, snapshot BaseSnapshot) error {
	store := &WorkspaceMetadataStore{r: r}
	return store.CommitBase(ws, state, snapshot, nil)
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

func envServerAddrOverride() string {
	return Runner{}.envServerAddrOverride()
}

func (r Runner) envServerAddrOverride() string {
	for _, key := range []string{"GS_SERVER_ADDR", "GITSLICE_GRPC_ADDR", "GITSLICE_SERVER_ADDR"} {
		if value := strings.TrimSpace(r.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func (r Runner) readEnvUserConfig() (UserConfig, bool, error) {
	token := strings.TrimSpace(r.Getenv("GS_TOKEN"))
	if token == "" {
		return UserConfig{}, false, nil
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return UserConfig{}, false, err
	}
	serverAddr := r.envServerAddrOverride()
	if serverAddr == "" {
		serverAddr = strings.TrimSpace(cfg.ServerAddr)
	}
	if serverAddr == "" {
		serverAddr = defaultServerAddr()
	}
	return UserConfig{
		ServerAddr: serverAddr,
		Token:      token,
		Aliases:    cfg.Aliases,
	}, true, nil
}

func (r Runner) readUserConfig() (UserConfig, error) {
	if cfg, ok, err := r.readEnvUserConfig(); err != nil {
		return cfg, err
	} else if ok {
		return cfg, nil
	}
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
	dir := filepath.Dir(r.userConfigPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
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
	return writeCompactJSONFile(filepath.Join(dir, "base_snapshot.json"), snapshot, 0o644)
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
	path := filepath.Join(dir, "conflicts.json")
	if err := writeJSONFile(path, state, 0o644); err != nil {
		return err
	}
	if len(state.Conflicts) > 0 {
		r.pinConflictContent(path)
	}
	return nil
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
	root, err := workspace.FindRoot(dir)
	return root, translateWorkspaceError(err)
}

func readJSONFile(path string, v any) error {
	return workspace.ReadJSONFile(path, v)
}

func writeJSONFile(path string, v any, mode fs.FileMode) error {
	return workspace.WriteJSONFile(path, v, mode)
}

func writeCompactJSONFile(path string, v any, mode fs.FileMode) error {
	return workspace.WriteCompactJSONFile(path, v, mode)
}

func writeAtomicFile(path string, data []byte, mode fs.FileMode) error {
	return workspace.WriteAtomicFile(path, data, mode)
}
