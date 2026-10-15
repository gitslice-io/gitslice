package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
)

func (s *ChangesetStore) ensurePathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, baseCommitID, p string) (*pathEntity, error) {
	entity, err := getCurrentPathEntityTx(ctx, tx, targetRef, p)
	if err == nil {
		return entity, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	entry, err := s.repository.getEntryAtCommitTx(ctx, tx, baseCommitID, p)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	kind := entry.Kind
	contentHash := ""
	mode := uint32(0)
	if kind == "file" {
		contentHash = entry.ContentHash
		mode = entry.Mode
	}
	entity, err = s.createEntityTx(ctx, tx, "", p, kind, contentHash, mode)
	if err != nil {
		return nil, err
	}
	if err := upsertCurrentPathEntityTx(ctx, tx, targetRef, *entity); err != nil {
		return nil, err
	}
	return entity, nil
}

func (s *ChangesetStore) createEntityTx(ctx context.Context, tx *sql.Tx, commitID, p, kind, contentHash string, mode uint32) (*pathEntity, error) {
	accountID, err := accountIDForPathTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	entityID, err := objectid.RandomID("ent")
	if err != nil {
		return nil, err
	}
	var created any
	if commitID != "" {
		created = commitID
	}
	_, err = tx.ExecContext(ctx, `
		insert into fs_entities(account_id, entity_id, kind, created_commit_id)
		values ($1, $2, $3, $4)
		on conflict do nothing
	`, accountID, entityID, kind, created)
	if err != nil {
		return nil, err
	}
	return &pathEntity{Path: p, AccountID: accountID, EntityID: entityID, Kind: kind, ContentHash: contentHash, Mode: mode}, nil
}

func insertFSEntitiesTx(ctx context.Context, tx *sql.Tx, created []stagedEntityCreation) error {
	if len(created) == 0 {
		return nil
	}
	accountIDs := make([]string, 0, len(created))
	entityIDs := make([]string, 0, len(created))
	kinds := make([]string, 0, len(created))
	createdCommitIDs := make([]string, 0, len(created))
	for _, item := range created {
		accountIDs = append(accountIDs, item.entity.AccountID)
		entityIDs = append(entityIDs, item.entity.EntityID)
		kinds = append(kinds, item.entity.Kind)
		createdCommitIDs = append(createdCommitIDs, item.createdCommitID)
	}
	_, err := tx.ExecContext(ctx, `
		with input as (
			select *
			from unnest(
				$1::text[], $2::text[], $3::text[], $4::text[]
			) with ordinality as input(account_id, entity_id, kind, created_commit_id, ord)
		),
		deduped as (
			select distinct on (account_id, entity_id)
			       account_id, entity_id, kind, created_commit_id, ord
			from input
			order by account_id, entity_id, ord
		)
		insert into fs_entities(account_id, entity_id, kind, created_commit_id)
		select deduped.account_id,
		       deduped.entity_id,
		       deduped.kind,
		       case when deduped.created_commit_id <> '' then deduped.created_commit_id end
		from deduped
		order by deduped.ord
		on conflict do nothing
	`, accountIDs, entityIDs, kinds, createdCommitIDs)
	return err
}

func accountIDForPathTx(ctx context.Context, tx *sql.Tx, p string) (string, error) {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return "", ErrInvalid
	}
	slug := strings.Split(trimmed, "/")[0]
	var accountID string
	err := tx.QueryRowContext(ctx, `
		select id
		from accounts
		where slug = $1
	`, slug).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return accountID, err
}

func accountIDsForPathsTx(ctx context.Context, tx *sql.Tx, paths []string) (map[string]string, error) {
	out := map[string]string{}
	slugs := accountSlugsForPaths(paths)
	if len(slugs) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `
		select slug, id
		from accounts
		where slug = any($1)
	`, slugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug, accountID string
		if err := rows.Scan(&slug, &accountID); err != nil {
			return nil, err
		}
		out[slug] = accountID
	}
	return out, rows.Err()
}

func accountSlugsForPaths(paths []string) []string {
	seen := map[string]struct{}{}
	for _, p := range paths {
		trimmed := strings.Trim(p, "/")
		if trimmed == "" {
			continue
		}
		slug := strings.Split(trimmed, "/")[0]
		seen[slug] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for slug := range seen {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

func getCurrentPathEntitiesTx(ctx context.Context, tx *sql.Tx, targetRef string, paths []string) (map[string]pathEntity, error) {
	out := make(map[string]pathEntity, len(paths))
	paths = uniqueSortedStrings(paths)
	if len(paths) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `
		select path, account_id, entity_id, kind, content_hash, mode
		from current_path_entities
		where target_ref = $1 and path = any($2)
	`, targetRef, paths)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var entity pathEntity
		var contentHash sql.NullString
		var mode sql.NullInt64
		if err := rows.Scan(&entity.Path, &entity.AccountID, &entity.EntityID, &entity.Kind, &contentHash, &mode); err != nil {
			return nil, err
		}
		entity.ContentHash = contentHash.String
		if mode.Valid && mode.Int64 > 0 {
			entity.Mode = uint32(mode.Int64)
		}
		out[entity.Path] = entity
	}
	return out, rows.Err()
}

func getCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, p string) (*pathEntity, error) {
	var entity pathEntity
	var contentHash sql.NullString
	var mode sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		select path, account_id, entity_id, kind, content_hash, mode
		from current_path_entities
		where target_ref = $1 and path = $2
	`, targetRef, p).Scan(&entity.Path, &entity.AccountID, &entity.EntityID, &entity.Kind, &contentHash, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	entity.ContentHash = contentHash.String
	if mode.Valid && mode.Int64 > 0 {
		entity.Mode = uint32(mode.Int64)
	}
	return &entity, nil
}

func upsertCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef string, entity pathEntity) error {
	var mode any
	if entity.Mode != 0 {
		mode = int(entity.Mode)
	}
	var contentHash any
	if entity.ContentHash != "" {
		contentHash = entity.ContentHash
	}
	_, err := tx.ExecContext(ctx, `
		insert into current_path_entities(target_ref, path, account_id, entity_id, kind, content_hash, mode, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, now())
		on conflict (target_ref, path) do update
		set account_id = excluded.account_id,
		    entity_id = excluded.entity_id,
		    kind = excluded.kind,
		    content_hash = excluded.content_hash,
		    mode = excluded.mode,
		    updated_at = now()
	`, targetRef, entity.Path, entity.AccountID, entity.EntityID, entity.Kind, contentHash, mode)
	return err
}

func upsertCurrentPathEntitiesTx(ctx context.Context, tx *sql.Tx, targetRef string, entities []pathEntity) error {
	if len(entities) == 0 {
		return nil
	}
	paths := make([]string, 0, len(entities))
	accountIDs := make([]string, 0, len(entities))
	entityIDs := make([]string, 0, len(entities))
	kinds := make([]string, 0, len(entities))
	contentHashes := make([]string, 0, len(entities))
	modes := make([]int64, 0, len(entities))
	for _, entity := range entities {
		paths = append(paths, entity.Path)
		accountIDs = append(accountIDs, entity.AccountID)
		entityIDs = append(entityIDs, entity.EntityID)
		kinds = append(kinds, entity.Kind)
		contentHashes = append(contentHashes, entity.ContentHash)
		modes = append(modes, int64(entity.Mode))
	}
	_, err := tx.ExecContext(ctx, `
		insert into current_path_entities(target_ref, path, account_id, entity_id, kind, content_hash, mode, updated_at)
		select $1::text,
		       input.path,
		       input.account_id,
		       input.entity_id,
		       input.kind,
		       case when input.content_hash <> '' then input.content_hash end,
		       case when input.mode > 0 then input.mode::integer end,
		       now()
		from unnest(
			$2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::bigint[]
		) as input(path, account_id, entity_id, kind, content_hash, mode)
		on conflict (target_ref, path) do update
		set account_id = excluded.account_id,
		    entity_id = excluded.entity_id,
		    kind = excluded.kind,
		    content_hash = excluded.content_hash,
		    mode = excluded.mode,
		    updated_at = now()
	`, targetRef, paths, accountIDs, entityIDs, kinds, contentHashes, modes)
	return err
}

func deleteCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, p string, recursive bool) error {
	if recursive {
		_, err := tx.ExecContext(ctx, `
			delete from current_path_entities
			where target_ref = $1
			  and (path = $2 or path like $3 escape '\')
		`, targetRef, p, likePrefixPattern(p))
		return err
	}
	_, err := tx.ExecContext(ctx, `
		delete from current_path_entities
		where target_ref = $1 and path = $2
	`, targetRef, p)
	return err
}

func deleteCurrentPathEntitiesTx(ctx context.Context, tx *sql.Tx, targetRef string, paths []string) error {
	paths = uniqueSortedStrings(paths)
	if len(paths) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		delete from current_path_entities
		where target_ref = $1 and path = any($2)
	`, targetRef, paths)
	return err
}

func moveCurrentPathEntityTx(ctx context.Context, tx *sql.Tx, targetRef, oldPath, newPath string, entity pathEntity) error {
	if entity.Kind == "directory" {
		_, err := tx.ExecContext(ctx, `
			update current_path_entities
			set path = $2 || substring(path from length($3) + 1),
			    updated_at = now()
			where target_ref = $1
			  and path <> $3
			  and (path = $3 or path like $4 escape '\')
		`, targetRef, newPath, oldPath, likePrefixPattern(oldPath))
		if err != nil {
			return err
		}
	}
	if err := deleteCurrentPathEntityTx(ctx, tx, targetRef, oldPath, false); err != nil {
		return err
	}
	return upsertCurrentPathEntityTx(ctx, tx, targetRef, entity)
}

func markEntityDeletedTx(ctx context.Context, tx *sql.Tx, entity pathEntity, commitID string) error {
	_, err := tx.ExecContext(ctx, `
		update fs_entities
		set deleted_commit_id = $3
		where account_id = $1 and entity_id = $2
	`, entity.AccountID, entity.EntityID, commitID)
	return err
}

func markEntitiesDeletedTx(ctx context.Context, tx *sql.Tx, deleted []stagedEntityDeletion) error {
	if len(deleted) == 0 {
		return nil
	}
	accountIDs := make([]string, 0, len(deleted))
	entityIDs := make([]string, 0, len(deleted))
	commitIDs := make([]string, 0, len(deleted))
	for _, item := range deleted {
		accountIDs = append(accountIDs, item.entity.AccountID)
		entityIDs = append(entityIDs, item.entity.EntityID)
		commitIDs = append(commitIDs, item.commitID)
	}
	_, err := tx.ExecContext(ctx, `
		update fs_entities entity
		set deleted_commit_id = input.commit_id
		from unnest(
			$1::text[], $2::text[], $3::text[]
		) as input(account_id, entity_id, commit_id)
		where entity.account_id = input.account_id
		  and entity.entity_id = input.entity_id
	`, accountIDs, entityIDs, commitIDs)
	return err
}

func insertEntityChangeTx(ctx context.Context, tx *sql.Tx, targetRef, commitID string, change entityChange, committedAt time.Time) error {
	source := change.Source
	if source == "" {
		source = "explicit"
	}
	confidence := change.Confidence
	if confidence == 0 {
		confidence = 100
	}
	var oldPath any
	if change.OldPath != "" {
		oldPath = change.OldPath
	}
	var contentHash any
	if change.ContentHash != "" {
		contentHash = change.ContentHash
	}
	var mode any
	if change.Mode != 0 {
		mode = int(change.Mode)
	}
	_, err := tx.ExecContext(ctx, `
		insert into commit_entity_changes(
			target_ref, commit_id, account_id, entity_id, kind, path, old_path,
			change_kind, source, confidence, content_hash, mode, committed_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		on conflict do nothing
	`, targetRef, commitID, change.Entity.AccountID, change.Entity.EntityID, change.Entity.Kind, change.Path, oldPath, change.ChangeKind, source, confidence, contentHash, mode, committedAt)
	return err
}

func insertEntityChangesTx(ctx context.Context, tx *sql.Tx, targetRef, commitID string, changes []entityChange, committedAt time.Time) error {
	if len(changes) == 0 {
		return nil
	}
	accountIDs := make([]string, 0, len(changes))
	entityIDs := make([]string, 0, len(changes))
	kinds := make([]string, 0, len(changes))
	paths := make([]string, 0, len(changes))
	oldPaths := make([]string, 0, len(changes))
	changeKinds := make([]string, 0, len(changes))
	sources := make([]string, 0, len(changes))
	confidences := make([]int64, 0, len(changes))
	contentHashes := make([]string, 0, len(changes))
	modes := make([]int64, 0, len(changes))
	for _, change := range changes {
		source := change.Source
		if source == "" {
			source = "explicit"
		}
		confidence := change.Confidence
		if confidence == 0 {
			confidence = 100
		}
		accountIDs = append(accountIDs, change.Entity.AccountID)
		entityIDs = append(entityIDs, change.Entity.EntityID)
		kinds = append(kinds, change.Entity.Kind)
		paths = append(paths, change.Path)
		oldPaths = append(oldPaths, change.OldPath)
		changeKinds = append(changeKinds, change.ChangeKind)
		sources = append(sources, source)
		confidences = append(confidences, int64(confidence))
		contentHashes = append(contentHashes, change.ContentHash)
		modes = append(modes, int64(change.Mode))
	}
	_, err := tx.ExecContext(ctx, `
		with input as (
			select *
			from unnest(
				$4::text[], $5::text[], $6::text[], $7::text[], $8::text[],
				$9::text[], $10::text[], $11::bigint[], $12::text[], $13::bigint[]
			) with ordinality as input(account_id, entity_id, kind, path, old_path, change_kind, source, confidence, content_hash, mode, ord)
		),
		deduped as (
			select distinct on (account_id, entity_id, path, change_kind)
			       account_id, entity_id, kind, path, old_path, change_kind,
			       source, confidence, content_hash, mode, ord
			from input
			order by account_id, entity_id, path, change_kind, ord
		)
		insert into commit_entity_changes(
			target_ref, commit_id, account_id, entity_id, kind, path, old_path,
			change_kind, source, confidence, content_hash, mode, committed_at
		)
		select $1::text,
		       $2::text,
		       deduped.account_id,
		       deduped.entity_id,
		       deduped.kind,
		       deduped.path,
		       case when deduped.old_path <> '' then deduped.old_path end,
		       deduped.change_kind,
		       deduped.source,
		       deduped.confidence::integer,
		       case when deduped.content_hash <> '' then deduped.content_hash end,
		       case when deduped.mode > 0 then deduped.mode::integer end,
		       $3::timestamptz
		from deduped
		order by deduped.ord
		on conflict do nothing
	`, targetRef, commitID, committedAt, accountIDs, entityIDs, kinds, paths, oldPaths, changeKinds, sources, confidences, contentHashes, modes)
	return err
}

func pathEntitiesFromMap(values map[string]pathEntity) []pathEntity {
	paths := make([]string, 0, len(values))
	for p := range values {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]pathEntity, 0, len(paths))
	for _, p := range paths {
		out = append(out, values[p])
	}
	return out
}

func stringsFromSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
