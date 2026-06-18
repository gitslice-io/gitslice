package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
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

func (s *RepositoryStore) ResolveCommitCandidates(ctx context.Context, filter storage.CommitResolveFilter) ([]*corev1.Commit, error) {
	refName := filter.RefName
	if refName == "" {
		refName = DefaultTargetRef
	}
	idPrefix := strings.TrimSpace(filter.IDPrefix)
	if idPrefix == "" {
		return nil, fmt.Errorf("%w: commit id prefix is required", ErrInvalid)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 20 {
		limit = 2
	}
	prefixes := normalizeCommitPathPrefixes(filter.PathPrefixes)
	refs := normalizeHistoryEntityRefs(filter.EntityRefs)

	sourceSQL := ""
	var args []any
	switch {
	case len(refs) > 0 && filter.IncludePrefixesWithEntities && len(prefixes) > 0:
		entityWhere, entityArgs := commitEntityRefWhere(refName, refs)
		pathWhere, pathArgs := commitPathPrefixWhere(refName, prefixes)
		pathWhere = shiftPostgresPlaceholders(pathWhere, len(entityArgs))
		args = append(args, entityArgs...)
		args = append(args, pathArgs...)
		sourceSQL = `
			select ce.commit_id, ce.committed_at
			from commit_entity_changes ce
			where ` + entityWhere + `
			union all
			select cp.commit_id, cp.committed_at
			from commit_changed_paths cp
			where ` + pathWhere
	case len(refs) > 0:
		where, whereArgs := commitEntityRefWhere(refName, refs)
		args = whereArgs
		sourceSQL = `
			select ce.commit_id, ce.committed_at
			from commit_entity_changes ce
			where ` + where
	case len(prefixes) > 0:
		where, whereArgs := commitPathPrefixWhere(refName, prefixes)
		args = whereArgs
		sourceSQL = `
			select cp.commit_id, cp.committed_at
			from commit_changed_paths cp
			where ` + where
	default:
		args = []any{refName}
		sourceSQL = `
			select cp.commit_id, cp.committed_at
			from commit_changed_paths cp
			where cp.target_ref = $1`
	}
	return s.resolveCommitCandidatesFromSource(ctx, sourceSQL, args, idPrefix, limit)
}

func (s *RepositoryStore) resolveCommitCandidatesFromSource(ctx context.Context, sourceSQL string, args []any, idPrefix string, limit int) ([]*corev1.Commit, error) {
	prefixArg := len(args) + 1
	limitArg := len(args) + 2
	args = append(args, idPrefix, limit)
	rows, err := s.db.QueryContext(ctx, `
		select c.id, c.parent_ids, c.root_tree_id, coalesce(c.author_subject_id, ''),
		       c.message, c.created_at, c.changed_paths, hits.committed_at
		from (
			select source.commit_id, max(source.committed_at) as committed_at
			from (`+sourceSQL+`) source
			group by source.commit_id
		) hits
		join commits c on c.id = hits.commit_id
		where c.id like $`+fmt.Sprint(prefixArg)+` || '%'
		order by hits.committed_at desc, c.id desc
		limit $`+fmt.Sprint(limitArg)+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commits := make([]*corev1.Commit, 0, limit)
	for rows.Next() {
		commit, _, err := scanCommitHitRow(rows)
		if err != nil {
			return nil, err
		}
		commits = append(commits, commit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return commits, nil
}

func (s *RepositoryStore) ListCommits(ctx context.Context, refName string, limit int) ([]*corev1.Commit, error) {
	page, err := s.ListCommitPage(ctx, refName, limit, "")
	if err != nil {
		return nil, err
	}
	return page.Commits, nil
}

func (s *RepositoryStore) ListCommitPage(ctx context.Context, refName string, limit int, pageToken string) (*CommitListPage, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	limit = normalizeCommitListLimit(limit)
	cursor, err := decodeCommitPageToken(pageToken)
	if err != nil {
		return nil, err
	}
	ref, err := s.GetRef(ctx, refName)
	if err != nil {
		return nil, err
	}
	commits := make([]*corev1.Commit, 0, limit+1)
	seen := map[string]struct{}{}
	collecting := cursor == nil
	foundCursor := cursor == nil
	for commitID := ref.CommitId; commitID != "" && len(commits) < limit+1; {
		if _, ok := seen[commitID]; ok {
			break
		}
		seen[commitID] = struct{}{}
		commit, err := s.GetCommit(ctx, commitID)
		if err != nil {
			return nil, err
		}
		if collecting {
			commits = append(commits, commit)
		} else if commit.Id == cursor.CommitID {
			collecting = true
			foundCursor = true
		}
		if len(commit.ParentIds) == 0 {
			break
		}
		commitID = commit.ParentIds[0]
	}
	if !foundCursor {
		return nil, fmt.Errorf("%w: page token is not in ref history", ErrInvalid)
	}
	nextToken := ""
	if len(commits) > limit {
		var err error
		nextToken, err = commitPageTokenForCommit(commits[limit-1])
		if err != nil {
			return nil, err
		}
		commits = commits[:limit]
	}
	return &CommitListPage{Commits: commits, NextPageToken: nextToken}, nil
}

func (s *RepositoryStore) ListCommitsByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int) ([]*corev1.Commit, error) {
	page, err := s.ListCommitPageByPathPrefixes(ctx, refName, prefixes, limit, "")
	if err != nil {
		return nil, err
	}
	return page.Commits, nil
}

func (s *RepositoryStore) ListCommitPageByPathPrefixes(ctx context.Context, refName string, prefixes []string, limit int, pageToken string) (*CommitListPage, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	limit = normalizeCommitListLimit(limit)
	cursor, err := decodeCommitPageToken(pageToken)
	if err != nil {
		return nil, err
	}
	prefixes = normalizeCommitPathPrefixes(prefixes)
	if len(prefixes) == 0 {
		return s.ListCommitPage(ctx, refName, limit, pageToken)
	}
	where, args := commitPathPrefixWhere(refName, prefixes)
	cursorWhere := ""
	if cursor != nil {
		committedAt, err := cursor.committedAt()
		if err != nil {
			return nil, err
		}
		committedAtArg := len(args) + 1
		commitIDArg := len(args) + 2
		cursorWhere = fmt.Sprintf("where hits.committed_at < $%d or (hits.committed_at = $%d and hits.commit_id < $%d)", committedAtArg, committedAtArg, commitIDArg)
		args = append(args, committedAt, cursor.CommitID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `
		select c.id, c.parent_ids, c.root_tree_id, coalesce(c.author_subject_id, ''),
		       c.message, c.created_at, c.changed_paths, hits.committed_at
		from (
			select cp.commit_id, max(cp.committed_at) as committed_at
			from commit_changed_paths cp
			where `+where+`
			group by cp.commit_id
		) hits
		join commits c on c.id = hits.commit_id
		`+cursorWhere+`
		order by hits.committed_at desc, c.id desc
		limit $`+fmt.Sprint(len(args))+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type commitHit struct {
		commit      *corev1.Commit
		committedAt time.Time
	}
	hits := make([]commitHit, 0, limit+1)
	for rows.Next() {
		commit, committedAt, err := scanCommitHitRow(rows)
		if err != nil {
			return nil, err
		}
		hits = append(hits, commitHit{commit: commit, committedAt: committedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	nextToken := ""
	if len(hits) > limit {
		var err error
		nextToken, err = encodeCommitPageToken(hits[limit-1].commit.Id, hits[limit-1].committedAt)
		if err != nil {
			return nil, err
		}
		hits = hits[:limit]
	}
	commits := make([]*corev1.Commit, 0, len(hits))
	for _, hit := range hits {
		commits = append(commits, hit.commit)
	}
	return &CommitListPage{Commits: commits, NextPageToken: nextToken}, nil
}

func (s *RepositoryStore) ListCommitPageByEntityRefs(ctx context.Context, refName string, refs []HistoryEntityRef, limit int, pageToken string) (*CommitListPage, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	limit = normalizeCommitListLimit(limit)
	cursor, err := decodeCommitPageToken(pageToken)
	if err != nil {
		return nil, err
	}
	refs = normalizeHistoryEntityRefs(refs)
	if len(refs) == 0 {
		return &CommitListPage{}, nil
	}
	where, args := commitEntityRefWhere(refName, refs)
	cursorWhere := ""
	if cursor != nil {
		committedAt, err := cursor.committedAt()
		if err != nil {
			return nil, err
		}
		committedAtArg := len(args) + 1
		commitIDArg := len(args) + 2
		cursorWhere = fmt.Sprintf("where hits.committed_at < $%d or (hits.committed_at = $%d and hits.commit_id < $%d)", committedAtArg, committedAtArg, commitIDArg)
		args = append(args, committedAt, cursor.CommitID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `
		select c.id, c.parent_ids, c.root_tree_id, coalesce(c.author_subject_id, ''),
		       c.message, c.created_at, c.changed_paths, hits.committed_at
		from (
			select ce.commit_id, max(ce.committed_at) as committed_at
			from commit_entity_changes ce
			where `+where+`
			group by ce.commit_id
		) hits
		join commits c on c.id = hits.commit_id
		`+cursorWhere+`
		order by hits.committed_at desc, c.id desc
		limit $`+fmt.Sprint(len(args))+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type commitHit struct {
		commit      *corev1.Commit
		committedAt time.Time
	}
	hits := make([]commitHit, 0, limit+1)
	for rows.Next() {
		commit, committedAt, err := scanCommitHitRow(rows)
		if err != nil {
			return nil, err
		}
		hits = append(hits, commitHit{commit: commit, committedAt: committedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	nextToken := ""
	if len(hits) > limit {
		var err error
		nextToken, err = encodeCommitPageToken(hits[limit-1].commit.Id, hits[limit-1].committedAt)
		if err != nil {
			return nil, err
		}
		hits = hits[:limit]
	}
	commits := make([]*corev1.Commit, 0, len(hits))
	for _, hit := range hits {
		commits = append(commits, hit.commit)
	}
	return &CommitListPage{Commits: commits, NextPageToken: nextToken}, nil
}

func (s *RepositoryStore) ListCommitPageByEntityRefsOrPathPrefixes(ctx context.Context, refName string, refs []HistoryEntityRef, prefixes []string, limit int, pageToken string) (*CommitListPage, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	refs = normalizeHistoryEntityRefs(refs)
	prefixes = normalizeCommitPathPrefixes(prefixes)
	if len(refs) == 0 {
		return s.ListCommitPageByPathPrefixes(ctx, refName, prefixes, limit, pageToken)
	}
	if len(prefixes) == 0 {
		return s.ListCommitPageByEntityRefs(ctx, refName, refs, limit, pageToken)
	}
	limit = normalizeCommitListLimit(limit)
	cursor, err := decodeCommitPageToken(pageToken)
	if err != nil {
		return nil, err
	}
	entityWhere, args := commitEntityRefWhere(refName, refs)
	pathWhere, pathArgs := commitPathPrefixWhere(refName, prefixes)
	pathOffset := len(args)
	pathWhere = shiftPostgresPlaceholders(pathWhere, pathOffset)
	args = append(args, pathArgs...)
	cursorWhere := ""
	if cursor != nil {
		committedAt, err := cursor.committedAt()
		if err != nil {
			return nil, err
		}
		committedAtArg := len(args) + 1
		commitIDArg := len(args) + 2
		cursorWhere = fmt.Sprintf("where hits.committed_at < $%d or (hits.committed_at = $%d and hits.commit_id < $%d)", committedAtArg, committedAtArg, commitIDArg)
		args = append(args, committedAt, cursor.CommitID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `
		select c.id, c.parent_ids, c.root_tree_id, coalesce(c.author_subject_id, ''),
		       c.message, c.created_at, c.changed_paths, hits.committed_at
		from (
			select source.commit_id, max(source.committed_at) as committed_at
			from (
				select ce.commit_id, ce.committed_at
				from commit_entity_changes ce
				where `+entityWhere+`
				union all
				select cp.commit_id, cp.committed_at
				from commit_changed_paths cp
				where `+pathWhere+`
			) source
			group by source.commit_id
		) hits
		join commits c on c.id = hits.commit_id
		`+cursorWhere+`
		order by hits.committed_at desc, c.id desc
		limit $`+fmt.Sprint(len(args))+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type commitHit struct {
		commit      *corev1.Commit
		committedAt time.Time
	}
	hits := make([]commitHit, 0, limit+1)
	for rows.Next() {
		commit, committedAt, err := scanCommitHitRow(rows)
		if err != nil {
			return nil, err
		}
		hits = append(hits, commitHit{commit: commit, committedAt: committedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	nextToken := ""
	if len(hits) > limit {
		var err error
		nextToken, err = encodeCommitPageToken(hits[limit-1].commit.Id, hits[limit-1].committedAt)
		if err != nil {
			return nil, err
		}
		hits = hits[:limit]
	}
	commits := make([]*corev1.Commit, 0, len(hits))
	for _, hit := range hits {
		commits = append(commits, hit.commit)
	}
	return &CommitListPage{Commits: commits, NextPageToken: nextToken}, nil
}

func (s *RepositoryStore) CurrentPathEntitiesByPrefixes(ctx context.Context, refName string, prefixes []string) ([]CurrentPathEntity, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	prefixes = normalizeCommitPathPrefixes(prefixes)
	if len(prefixes) == 0 {
		return nil, nil
	}
	where, args := currentPathEntityPrefixWhere(refName, prefixes)
	rows, err := s.db.QueryContext(ctx, `
		select path, account_id, entity_id, kind, coalesce(content_hash, ''), coalesce(mode, 0)
		from current_path_entities cpe
		where `+where+`
		order by path
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CurrentPathEntity
	for rows.Next() {
		var entity CurrentPathEntity
		var mode int
		if err := rows.Scan(&entity.Path, &entity.AccountID, &entity.EntityID, &entity.Kind, &entity.ContentHash, &mode); err != nil {
			return nil, err
		}
		if mode > 0 {
			entity.Mode = uint32(mode)
		}
		out = append(out, entity)
	}
	return out, rows.Err()
}

func (s *RepositoryStore) CurrentPathEntitiesByPaths(ctx context.Context, refName string, paths []string) ([]CurrentPathEntity, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	seen := map[string]struct{}{}
	ordered := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		ordered = append(ordered, p)
	}
	if len(ordered) == 0 {
		return nil, nil
	}
	args := []any{refName}
	placeholders := make([]string, 0, len(ordered))
	for _, p := range ordered {
		args = append(args, p)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	rows, err := s.db.QueryContext(ctx, `
		select path, account_id, entity_id, kind, coalesce(content_hash, ''), coalesce(mode, 0)
		from current_path_entities
		where target_ref = $1 and path in (`+strings.Join(placeholders, ", ")+`)
		order by path
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CurrentPathEntity
	for rows.Next() {
		var entity CurrentPathEntity
		var mode int
		if err := rows.Scan(&entity.Path, &entity.AccountID, &entity.EntityID, &entity.Kind, &entity.ContentHash, &mode); err != nil {
			return nil, err
		}
		if mode > 0 {
			entity.Mode = uint32(mode)
		}
		out = append(out, entity)
	}
	return out, rows.Err()
}

func scanCommitRow(rows *sql.Rows) (*corev1.Commit, error) {
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
	return &commit, nil
}

func scanCommitHitRow(rows *sql.Rows) (*corev1.Commit, time.Time, error) {
	var commit corev1.Commit
	var parentJSON, changedJSON []byte
	var createdAt, committedAt time.Time
	if err := rows.Scan(&commit.Id, &parentJSON, &commit.RootTreeId, &commit.Author, &commit.Message, &createdAt, &changedJSON, &committedAt); err != nil {
		return nil, time.Time{}, err
	}
	commit.CreatedAt = formatTime(createdAt)
	if err := decodeJSON(parentJSON, &commit.ParentIds); err != nil {
		return nil, time.Time{}, err
	}
	if err := decodeJSON(changedJSON, &commit.ChangedPaths); err != nil {
		return nil, time.Time{}, err
	}
	return &commit, committedAt, nil
}

func normalizeCommitListLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 50
	}
	return limit
}

func commitPageTokenForCommit(commit *corev1.Commit) (string, error) {
	if commit == nil || commit.Id == "" {
		return "", fmt.Errorf("%w: commit page token cannot be built from empty commit", ErrInvalid)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, commit.CreatedAt)
	if err != nil {
		return "", fmt.Errorf("%w: invalid commit timestamp: %v", ErrInvalid, err)
	}
	return encodeCommitPageToken(commit.Id, createdAt)
}

func encodeCommitPageToken(commitID string, committedAt time.Time) (string, error) {
	if strings.TrimSpace(commitID) == "" || committedAt.IsZero() {
		return "", fmt.Errorf("%w: invalid commit page cursor", ErrInvalid)
	}
	raw, err := json.Marshal(commitPageCursor{
		CommitID:    commitID,
		CommittedAt: committedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCommitPageToken(token string) (*commitPageCursor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	var cursor commitPageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	if strings.TrimSpace(cursor.CommitID) == "" || strings.TrimSpace(cursor.CommittedAt) == "" {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	if _, err := cursor.committedAt(); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func (c commitPageCursor) committedAt() (time.Time, error) {
	committedAt, err := time.Parse(time.RFC3339Nano, c.CommittedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	return committedAt, nil
}

func normalizeCommitPathPrefixes(prefixes []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(prefix, "/")
		if prefix == "" {
			prefix = "/"
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

func commitPathPrefixWhere(refName string, prefixes []string) (string, []any) {
	args := []any{refName}
	clauses := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix == "/" {
			clauses = append(clauses, "true")
			continue
		}
		exact := len(args) + 1
		lower := len(args) + 2
		clauses = append(clauses, fmt.Sprintf("(cp.path = $%d or left(cp.path, length($%d)) = $%d)", exact, lower, lower))
		args = append(args, prefix, strings.TrimRight(prefix, "/")+"/")
	}
	return "cp.target_ref = $1 and (" + strings.Join(clauses, " or ") + ")", args
}

func currentPathEntityPrefixWhere(refName string, prefixes []string) (string, []any) {
	args := []any{refName}
	clauses := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix == "/" {
			clauses = append(clauses, "true")
			continue
		}
		exact := len(args) + 1
		lower := len(args) + 2
		clauses = append(clauses, fmt.Sprintf("(cpe.path = $%d or left(cpe.path, length($%d)) = $%d)", exact, lower, lower))
		args = append(args, prefix, strings.TrimRight(prefix, "/")+"/")
	}
	return "cpe.target_ref = $1 and (" + strings.Join(clauses, " or ") + ")", args
}

func normalizeHistoryEntityRefs(refs []HistoryEntityRef) []HistoryEntityRef {
	seen := map[string]struct{}{}
	out := make([]HistoryEntityRef, 0, len(refs))
	for _, ref := range refs {
		ref.AccountID = strings.TrimSpace(ref.AccountID)
		ref.EntityID = strings.TrimSpace(ref.EntityID)
		if ref.AccountID == "" || ref.EntityID == "" {
			continue
		}
		key := ref.AccountID + "\x00" + ref.EntityID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AccountID == out[j].AccountID {
			return out[i].EntityID < out[j].EntityID
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

func commitEntityRefWhere(refName string, refs []HistoryEntityRef) (string, []any) {
	args := []any{refName}
	clauses := make([]string, 0, len(refs))
	for _, ref := range refs {
		accountArg := len(args) + 1
		entityArg := len(args) + 2
		clauses = append(clauses, fmt.Sprintf("(ce.account_id = $%d and ce.entity_id = $%d)", accountArg, entityArg))
		args = append(args, ref.AccountID, ref.EntityID)
	}
	return "ce.target_ref = $1 and (" + strings.Join(clauses, " or ") + ")", args
}

func shiftPostgresPlaceholders(query string, offset int) string {
	if offset == 0 {
		return query
	}
	var b strings.Builder
	for i := 0; i < len(query); i++ {
		if query[i] != '$' || i+1 >= len(query) || query[i+1] < '0' || query[i+1] > '9' {
			b.WriteByte(query[i])
			continue
		}
		j := i + 1
		n := 0
		for j < len(query) && query[j] >= '0' && query[j] <= '9' {
			n = n*10 + int(query[j]-'0')
			j++
		}
		b.WriteString(fmt.Sprintf("$%d", n+offset))
		i = j - 1
	}
	return b.String()
}

func (s *RepositoryStore) GetFile(ctx context.Context, commitID, p string) (*FileEntry, error) {
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

func rootTreeIDForCommitTx(ctx context.Context, tx *sql.Tx, commitID string) (string, error) {
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
	rootTreeID, err := rootTreeIDForCommitTx(ctx, tx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) getEntryAtCommitTx(ctx context.Context, tx *sql.Tx, commitID, p string) (*TreeEntry, error) {
	rootTreeID, err := rootTreeIDForCommitTx(ctx, tx, commitID)
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
