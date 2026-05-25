package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type RepositoryStore struct {
	db    *sql.DB
	trees *treestore.Store
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

func (s *RepositoryStore) GetOrCreateGitImport(ctx context.Context, subjectID, source, mountPath string, sliceRef *corev1.SliceRef, sliceID, targetRef, mode string, totalCommits int) (*GitImportRecord, error) {
	id, err := objectid.RandomID("gimp")
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		insert into git_imports(
			id, subject_id, source, mount_path, authoring_account, authoring_slice,
			authoring_slice_id, target_ref, mode, status, total_commits, updated_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'running', $10, now())
		on conflict (source, mount_path, authoring_slice_id, target_ref, mode) do update
		set status = 'running', total_commits = $10, updated_at = now()
	`, id, subjectID, source, mountPath, sliceRef.Account, sliceRef.Slice, sliceID, targetRef, mode, totalCommits)
	if err != nil {
		return nil, err
	}
	return s.GetGitImport(ctx, source, mountPath, sliceID, targetRef, mode)
}

func (s *RepositoryStore) GetGitImport(ctx context.Context, source, mountPath, sliceID, targetRef, mode string) (*GitImportRecord, error) {
	var out GitImportRecord
	var lastGit, finalNative sql.NullString
	err := s.db.QueryRowContext(ctx, `
		select id, subject_id, source, mount_path, authoring_account, authoring_slice,
		       authoring_slice_id, target_ref, mode, status, total_commits, imported_count,
		       last_git_commit_id, final_native_commit_id
		from git_imports
		where source = $1 and mount_path = $2 and authoring_slice_id = $3 and target_ref = $4 and mode = $5
	`, source, mountPath, sliceID, targetRef, mode).Scan(
		&out.ID, &out.SubjectID, &out.Source, &out.MountPath, &out.AuthoringAccount, &out.AuthoringSlice,
		&out.AuthoringSliceID, &out.TargetRef, &out.Mode, &out.Status, &out.TotalCommits, &out.ImportedCount,
		&lastGit, &finalNative,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out.LastGitCommitID = lastGit.String
	out.FinalNativeCommitID = finalNative.String
	return &out, nil
}

func (s *RepositoryStore) ListGitImportCommits(ctx context.Context, importID string) ([]GitImportedCommitRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		select import_id, git_commit_id, native_commit_id, message, position, changed_path_count
		from git_import_commits
		where import_id = $1
		order by position
	`, importID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitImportedCommitRecord
	for rows.Next() {
		var row GitImportedCommitRecord
		if err := rows.Scan(&row.ImportID, &row.GitCommitID, &row.NativeCommitID, &row.Message, &row.Position, &row.ChangedPathCount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *RepositoryStore) RecordGitImportCommit(ctx context.Context, importID, gitCommitID, nativeCommitID, message string, position, changedPathCount int) error {
	_, err := s.db.ExecContext(ctx, `
		insert into git_import_commits(import_id, git_commit_id, native_commit_id, message, position, changed_path_count)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (import_id, git_commit_id) do update
		set native_commit_id = excluded.native_commit_id,
		    message = excluded.message,
		    position = excluded.position,
		    changed_path_count = excluded.changed_path_count
	`, importID, gitCommitID, nativeCommitID, message, position, changedPathCount)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		update git_imports
		set imported_count = (
				select count(*) from git_import_commits where import_id = $1
			),
			last_git_commit_id = $2,
			final_native_commit_id = $3,
			updated_at = now()
		where id = $1
	`, importID, gitCommitID, nativeCommitID)
	return err
}

func (s *RepositoryStore) CompleteGitImport(ctx context.Context, importID, finalNativeCommitID string) error {
	_, err := s.db.ExecContext(ctx, `
		update git_imports
		set status = 'completed', final_native_commit_id = $2, updated_at = now()
		where id = $1
	`, importID, finalNativeCommitID)
	return err
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

func (s *RepositoryStore) ListCommits(ctx context.Context, refName string, limit int) ([]*corev1.Commit, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	ref, err := s.GetRef(ctx, refName)
	if err != nil {
		return nil, err
	}
	commits := make([]*corev1.Commit, 0, limit)
	seen := map[string]struct{}{}
	for commitID := ref.CommitId; commitID != "" && len(commits) < limit; {
		if _, ok := seen[commitID]; ok {
			break
		}
		seen[commitID] = struct{}{}
		commit, err := s.GetCommit(ctx, commitID)
		if err != nil {
			return nil, err
		}
		commits = append(commits, commit)
		if len(commit.ParentIds) == 0 {
			break
		}
		commitID = commit.ParentIds[0]
	}
	return commits, nil
}

func (s *RepositoryStore) GetFile(ctx context.Context, commitID, p string) (*FileEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommit(ctx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) GetEntry(ctx context.Context, commitID, p string) (*TreeEntry, error) {
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

func (s *RepositoryStore) rootTreeIDForCommit(ctx context.Context, commitID string) (string, error) {
	var rootTreeID string
	err := s.db.QueryRowContext(ctx, `
		select root_tree_id
		from commits
		where id = $1
	`, commitID).Scan(&rootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rootTreeID, err
}

func (s *RepositoryStore) rootTreeIDForCommitTx(ctx context.Context, tx *sql.Tx, commitID string) (string, error) {
	var rootTreeID string
	err := tx.QueryRowContext(ctx, `
		select root_tree_id
		from commits
		where id = $1
	`, commitID).Scan(&rootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rootTreeID, err
}

func (s *RepositoryStore) getFileAtCommitTx(ctx context.Context, tx *sql.Tx, commitID, p string) (*FileEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommitTx(ctx, tx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) getEntryAtCommitTx(ctx context.Context, tx *sql.Tx, commitID, p string) (*TreeEntry, error) {
	rootTreeID, err := s.rootTreeIDForCommitTx(ctx, tx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getEntryFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) getFileFromTree(ctx context.Context, rootTreeID, p string) (*FileEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entry, err := s.trees.GetFile(ctx, rootTreeID, p)
	if errors.Is(err, treestore.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	file := fileEntryFromTree(*entry)
	return &file, nil
}

func (s *RepositoryStore) getEntryFromTree(ctx context.Context, rootTreeID, p string) (*TreeEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entry, err := s.trees.GetEntry(ctx, rootTreeID, p)
	if errors.Is(err, treestore.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := treeEntryFromTree(*entry)
	return &out, nil
}

func (s *RepositoryStore) listDirectoryFromTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entries, err := s.trees.ListDirectory(ctx, rootTreeID, p)
	if err != nil {
		return nil, err
	}
	out := make([]TreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, treeEntryFromTree(entry))
	}
	return out, nil
}

func (s *RepositoryStore) listFilesFromTree(ctx context.Context, rootTreeID, prefix string) ([]FileEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	files, err := s.trees.ListFiles(ctx, rootTreeID, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(files))
	for _, file := range files {
		out = append(out, fileEntryFromTree(file))
	}
	return out, nil
}
