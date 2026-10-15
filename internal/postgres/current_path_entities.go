package postgres

import (
	"context"
	"fmt"
	"strings"
)

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
		order by path collate "C"
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
		order by path collate "C"
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
