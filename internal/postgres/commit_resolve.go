package postgres

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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
