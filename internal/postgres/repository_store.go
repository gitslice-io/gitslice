package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type RepositoryStore struct {
	db    *sql.DB
	trees *treestore.Store
}

type CommitListPage = storage.CommitListPage

type HistoryEntityRef = storage.HistoryEntityRef

type CurrentPathEntity = storage.CurrentPathEntity

type commitPageCursor struct {
	CommitID    string `json:"commit_id"`
	CommittedAt string `json:"committed_at"`
}

func (s *RepositoryStore) GetRef(ctx context.Context, name string) (*corev1.Ref, error) {
	var ref corev1.Ref
	var updatedAt time.Time
	err := s.db.QueryRowContext(ctx, `
		select name, commit_id, updated_at, coalesce(updated_by, '')
		from refs
		where name = $1
	`, name).Scan(&ref.Name, &ref.CommitId, &updatedAt, &ref.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ref.UpdatedAt = formatTime(updatedAt)
	return &ref, nil
}

func (s *RepositoryStore) GetCommit(ctx context.Context, commitID string) (*corev1.Commit, error) {
	var commit corev1.Commit
	var parentJSON, changedJSON []byte
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		select id, parent_ids, root_tree_id, coalesce(author_subject_id, ''),
		       message, created_at, changed_paths
		from commits
		where id = $1
	`, commitID).Scan(&commit.Id, &parentJSON, &commit.RootTreeId, &commit.Author, &commit.Message, &createdAt, &changedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	commit.CreatedAt = formatTime(createdAt)
	if err := decodeJSON(parentJSON, &commit.ParentIds); err != nil {
		return nil, err
	}
	if err := decodeJSON(changedJSON, &commit.ChangedPaths); err != nil {
		return nil, err
	}
	return &commit, nil
}

// GetCommits reads many commits in one query.
func (s *RepositoryStore) GetCommits(ctx context.Context, commitIDs []string) (map[string]*corev1.Commit, error) {
	out := map[string]*corev1.Commit{}
	if len(commitIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, parent_ids, root_tree_id, coalesce(author_subject_id, ''),
		       message, created_at, changed_paths
		from commits
		where id = any($1)
	`, commitIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var commit corev1.Commit
		var parentJSON, changedJSON []byte
		var createdAt time.Time
		if err := rows.Scan(&commit.Id, &parentJSON, &commit.RootTreeId, &commit.Author, &commit.Message, &createdAt, &changedJSON); err != nil {
			return nil, err
		}
		commit.CreatedAt = formatTime(createdAt)
		if err := decodeJSON(parentJSON, &commit.ParentIds); err != nil {
			return nil, err
		}
		if err := decodeJSON(changedJSON, &commit.ChangedPaths); err != nil {
			return nil, err
		}
		out[commit.Id] = &commit
	}
	return out, rows.Err()
}

func (s *RepositoryStore) GetFile(ctx context.Context, commitID, p string) (*FileEntry, error) {
	if entry, handled, err := s.getFileFromMaterializedHead(ctx, DefaultTargetRef, commitID, p); handled {
		return entry, err
	}
	rootTreeID, err := s.rootTreeIDForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) RootTreeForCommit(ctx context.Context, commitID string) (string, error) {
	return s.rootTreeIDForCommit(ctx, commitID)
}

func (s *RepositoryStore) GetFileAtTree(ctx context.Context, rootTreeID, p string) (*FileEntry, error) {
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) GetEntryAtTree(ctx context.Context, rootTreeID, p string) (*TreeEntry, error) {
	return s.getEntryFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) ListDirectoryAtTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error) {
	return s.listDirectoryFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) ListTreeAtTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entries, err := s.trees.ListTree(ctx, rootTreeID, p)
	if err != nil {
		return nil, err
	}
	out := make([]TreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, treeEntryFromTree(entry))
	}
	return out, nil
}

func (s *RepositoryStore) GetEntry(ctx context.Context, commitID, p string) (*TreeEntry, error) {
	if entry, handled, err := s.getEntryFromMaterializedHead(ctx, DefaultTargetRef, commitID, p); handled {
		return entry, err
	}
	rootTreeID, err := s.rootTreeIDForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getEntryFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) ListDirectory(ctx context.Context, commitID, p string) ([]TreeEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	return s.listDirectoryFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) ListFiles(ctx context.Context, commitID, prefix string) ([]FileEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	return s.listFilesFromTree(ctx, rootTreeID, prefix)
}

// FileChanges reads the history index's file rows for the given commits.
func (s *RepositoryStore) FileChanges(ctx context.Context, refName string, commitIDs []string) ([]storage.FileChange, error) {
	if len(commitIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select commit_id, path, coalesce(old_path, ''), change_kind, coalesce(content_hash, '')
		from commit_entity_changes
		where target_ref = $1 and commit_id = any($2) and kind = 'file'
	`, refName, commitIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.FileChange
	for rows.Next() {
		var change storage.FileChange
		if err := rows.Scan(&change.CommitID, &change.Path, &change.OldPath, &change.ChangeKind, &change.ContentHash); err != nil {
			return nil, err
		}
		out = append(out, change)
	}
	return out, rows.Err()
}
