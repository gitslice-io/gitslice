package gitcompat

import (
	"encoding/json"
	"os"
	"path/filepath"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// projectionVersion identifies the projection algorithm and the state file
// layout. Bumping it makes every cached projection rebuild from scratch.
const projectionVersion = 4

const (
	projectionStateFile = "gitslice_projection.json"
	projectedBranch     = "refs/heads/main"
	emptyGitTree        = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	noreplyDomain       = "users.noreply.gitslice.io"
	commitTrailer       = "Gitslice-Commit"
	gitCommitTrailer    = "Git-Commit"

	// Listing and blob reads are latency-bound object-store round trips, so a
	// cold build runs many at once.
	projectionListConcurrency = 48
	projectionBlobConcurrency = 48
)

type projectionState struct {
	Version        int    `json:"version"`
	Account        string `json:"account"`
	Slice          string `json:"slice"`
	SliceID        string `json:"slice_id"`
	DefinitionHash string `json:"definition_hash"`
	// NativeHeadID is the head of refs/global/main the projection reflects.
	NativeHeadID string `json:"native_head_id"`
	// GitHead is the projected head commit, "" while the history is empty.
	GitHead string `json:"git_head"`
	// Commits pairs each projected commit with its native commit, oldest first.
	Commits []projectedCommit `json:"commits"`
	// Files is the projected tree at GitHead, keyed by Git path.
	Files map[string]projectedFile `json:"files"`
	// Tags caches each native tag's projected commit by tag name.
	Tags map[string]projectedCommit `json:"tags,omitempty"`
	// HistoryPacks names the packs holding the projected history (commits and
	// trees, plus any files written with them), in the order they were built.
	// A mirror copies exactly these; packs of hydrated file contents are
	// excluded because the native store already holds those files.
	HistoryPacks []string `json:"history_packs,omitempty"`
}

type projectedCommit struct {
	Native string `json:"native"`
	Git    string `json:"git"`
}

type projectedFile struct {
	Mode        string `json:"mode"`
	ContentHash string `json:"content_hash"`
	// Blob is the Git blob id, known once the content has been written.
	Blob string `json:"blob,omitempty"`
}

type fileChange struct {
	Path        string // Git path, no leading slash
	Delete      bool
	Mode        string
	ContentHash string
	Size        int64
}

type pendingCommit struct {
	native  *corev1.Commit
	changes []fileChange
	// imported is set when the native commit was published by a Git import.
	imported *storage.GitImportedCommitRecord
}

func newProjectionState(account, sliceSlug string, slice *corev1.Slice) *projectionState {
	return &projectionState{
		Version:        projectionVersion,
		Account:        account,
		Slice:          sliceSlug,
		SliceID:        slice.Id,
		DefinitionHash: slice.DefinitionHash,
		Files:          map[string]projectedFile{},
	}
}

func (s *projectionState) projection() *Projection {
	history := make(map[string]string, len(s.Commits))
	for _, commit := range s.Commits {
		history[commit.Git] = commit.Native
	}
	return &Projection{
		Account:        s.Account,
		Slice:          s.Slice,
		SliceID:        s.SliceID,
		DefinitionHash: s.DefinitionHash,
		NativeCommitID: s.NativeHeadID,
		GitCommitID:    s.GitHead,
		history:        history,
	}
}

func loadProjectionState(repoPath string) *projectionState {
	data, err := os.ReadFile(filepath.Join(repoPath, projectionStateFile))
	if err != nil {
		return nil
	}
	var state projectionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

func writeProjectionState(repoPath string, state *projectionState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(repoPath, ".state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(repoPath, projectionStateFile))
}
