package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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

// ListCommitChain walks the first-parent chain of refName inside one query, so
// the head and the commits come from a single snapshot. It reads only commit
// rows (written in the publish transaction), never the derived path indexes,
// which may lag the ref.
func (s *RepositoryStore) ListCommitChain(ctx context.Context, refName, stopCommitID string, prefixes []string) (*storage.CommitChain, error) {
	if refName == "" {
		refName = DefaultTargetRef
	}
	prefixes = normalizeCommitPathPrefixes(prefixes)
	prefixJSON, err := encodeJSON(prefixes)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		with recursive chain(id, depth) as (
			select r.commit_id, 0
			from refs r
			where r.name = $1
			union all
			select c.parent_ids->>0, chain.depth + 1
			from chain
			join commits c on c.id = chain.id
			where chain.id <> $2
			  and jsonb_array_length(c.parent_ids) > 0
		)
		select c.id, c.parent_ids, c.root_tree_id, coalesce(c.author_subject_id, ''),
		       c.message, c.created_at, c.changed_paths,
		       chain.depth = 0 as is_head,
		       chain.id = $2 as is_stop
		from chain
		join commits c on c.id = chain.id
		where chain.depth = 0
		   or chain.id = $2
		   or exists (
			select 1
			from jsonb_array_elements_text(c.changed_paths) changed(path)
			cross join jsonb_array_elements_text($3::jsonb) prefix(path)
			where changed.path = prefix.path
			   or prefix.path = '/'
			   or starts_with(changed.path, prefix.path || '/')
			   or starts_with(prefix.path, changed.path || '/')
		   )
		order by chain.depth desc
	`, refName, stopCommitID, string(prefixJSON))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chain := &storage.CommitChain{}
	var touching []*corev1.Commit
	for rows.Next() {
		var commit corev1.Commit
		var parentJSON, changedJSON []byte
		var createdAt time.Time
		var isHead, isStop bool
		if err := rows.Scan(&commit.Id, &parentJSON, &commit.RootTreeId, &commit.Author, &commit.Message, &createdAt, &changedJSON, &isHead, &isStop); err != nil {
			return nil, err
		}
		commit.CreatedAt = formatTime(createdAt)
		if err := decodeJSON(parentJSON, &commit.ParentIds); err != nil {
			return nil, err
		}
		if err := decodeJSON(changedJSON, &commit.ChangedPaths); err != nil {
			return nil, err
		}
		if isHead {
			chain.HeadCommitID = commit.Id
		}
		if isStop && stopCommitID != "" {
			chain.FoundStop = true
			continue
		}
		if commitTouchesPrefixes(commit.ChangedPaths, prefixes) {
			touching = append(touching, &commit)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if chain.HeadCommitID == "" {
		return nil, ErrNotFound
	}
	chain.Commits = touching
	return chain, nil
}

func (s *RepositoryStore) CommitAncestry(ctx context.Context, startCommitID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1 << 30
	}
	rows, err := s.db.QueryContext(ctx, `
		with recursive chain(id, depth) as (
			select c.id, 0 from commits c where c.id = $1
			union all
			select c.parent_ids->>0, chain.depth + 1
			from chain
			join commits c on c.id = chain.id
			where jsonb_array_length(c.parent_ids) > 0 and chain.depth + 1 < $2
		)
		select id from chain order by depth
	`, startCommitID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// commitTouchesPrefixes reports whether any changed path lies inside a prefix
// or is an ancestor directory of one (for example an account root change).
func commitTouchesPrefixes(changed, prefixes []string) bool {
	for _, p := range changed {
		for _, prefix := range prefixes {
			if prefix == "/" || p == prefix ||
				strings.HasPrefix(p, prefix+"/") || strings.HasPrefix(prefix, p+"/") {
				return true
			}
		}
	}
	return false
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
