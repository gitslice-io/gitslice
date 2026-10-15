package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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

const gitImportCommitColumns = `import_id, git_commit_id, native_commit_id, message, position, changed_path_count,
		author_name, author_email, authored_at, full_message`

func scanGitImportCommit(rows *sql.Rows) (GitImportedCommitRecord, error) {
	var row GitImportedCommitRecord
	var authoredAt sql.NullTime
	if err := rows.Scan(&row.ImportID, &row.GitCommitID, &row.NativeCommitID, &row.Message, &row.Position, &row.ChangedPathCount,
		&row.AuthorName, &row.AuthorEmail, &authoredAt, &row.FullMessage); err != nil {
		return row, err
	}
	if authoredAt.Valid {
		row.AuthoredAt = formatTime(authoredAt.Time)
	}
	return row, nil
}

func (s *RepositoryStore) ListGitImportCommits(ctx context.Context, importID string) ([]GitImportedCommitRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		select `+gitImportCommitColumns+`
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
		row, err := scanGitImportCommit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *RepositoryStore) GitImportsForCommits(ctx context.Context, nativeCommitIDs []string) (map[string]GitImportedCommitRecord, error) {
	out := map[string]GitImportedCommitRecord{}
	if len(nativeCommitIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select `+gitImportCommitColumns+`
		from git_import_commits
		where native_commit_id = any($1)
		order by created_at
	`, nativeCommitIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanGitImportCommit(rows)
		if err != nil {
			return nil, err
		}
		out[row.NativeCommitID] = row
	}
	return out, rows.Err()
}

func (s *RepositoryStore) ChangesetsForCommits(ctx context.Context, commitIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(commitIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `select commit_id, id from changesets where commit_id = any($1)`, commitIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var commitID, changesetID string
		if err := rows.Scan(&commitID, &changesetID); err != nil {
			return nil, err
		}
		out[commitID] = changesetID
	}
	return out, rows.Err()
}

func (s *RepositoryStore) RecordGitImportCommit(ctx context.Context, record GitImportedCommitRecord) error {
	var authoredAt any
	if record.AuthoredAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, record.AuthoredAt)
		if err != nil {
			return fmt.Errorf("%w: authored_at %q: %v", ErrInvalid, record.AuthoredAt, err)
		}
		authoredAt = parsed
	}
	_, err := s.db.ExecContext(ctx, `
		insert into git_import_commits(import_id, git_commit_id, native_commit_id, message, position, changed_path_count,
			author_name, author_email, authored_at, full_message)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		on conflict (import_id, git_commit_id) do update
		set native_commit_id = excluded.native_commit_id,
		    message = excluded.message,
		    position = excluded.position,
		    changed_path_count = excluded.changed_path_count,
		    author_name = excluded.author_name,
		    author_email = excluded.author_email,
		    authored_at = excluded.authored_at,
		    full_message = excluded.full_message
	`, record.ImportID, record.GitCommitID, record.NativeCommitID, record.Message, record.Position, record.ChangedPathCount,
		record.AuthorName, record.AuthorEmail, authoredAt, record.FullMessage)
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
	`, record.ImportID, record.GitCommitID, record.NativeCommitID)
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
