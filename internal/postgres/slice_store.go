package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type SliceStore struct {
	db      *sql.DB
	Secrets *secretbox.Box
}

func (s *SliceStore) Create(ctx context.Context, subjectID string, ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) (*corev1.Slice, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	includedPaths, visibility, requiredApprovals, requiredChecks, err = s.ValidateDefinition(ref, includedPaths, visibility, requiredApprovals, requiredChecks)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var accountID string
	err = tx.QueryRowContext(ctx, `select id from accounts where slug = $1`, ref.Account).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `
		select slices.id
		from slices
		where slices.account_id = $1 and slices.slug = $2
	`, accountID, ref.Slice).Scan(&existingID)
	if err == nil {
		return nil, fmt.Errorf("%w: slice %s/%s already exists", ErrConflict, ref.Account, ref.Slice)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var aliasOf string
	err = tx.QueryRowContext(ctx, `select slice_id from slice_aliases where account_id = $1 and slug = $2`, accountID, ref.Slice).Scan(&aliasOf)
	if err == nil {
		return nil, fmt.Errorf("%w: slice name %s/%s is reserved: a slice was renamed from it", ErrConflict, ref.Account, ref.Slice)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	id := sliceID(ref.Account, ref.Slice)
	includedJSON, err := encodeJSON(includedPaths)
	if err != nil {
		return nil, err
	}
	requiredChecksJSON, err := encodeJSON(requiredChecks)
	if err != nil {
		return nil, err
	}
	definitionHash := definitionHash(id, 1, includedPaths, visibility, requiredApprovals, requiredChecks)
	_, err = tx.ExecContext(ctx, `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, required_approvals, required_checks, created_at, updated_at)
		values ($1, $2, $3, 1, $4, $5, $6, $7, $8, now(), now())
	`, id, accountID, ref.Slice, definitionHash, visibility, includedJSON, requiredApprovals, requiredChecksJSON)
	if err != nil {
		return nil, err
	}
	if err := appendSliceDefinitionVersionTx(ctx, tx, id, 1, definitionHash, visibility, includedPaths, requiredApprovals, requiredChecks, subjectID); err != nil {
		return nil, err
	}
	if err := syncSliceIncludedPathsTx(ctx, tx, id, includedPaths); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *SliceStore) ValidateDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error) {
	return validateSliceDefinition(ref, includedPaths, visibility, requiredApprovals, requiredChecks)
}

func (s *SliceStore) Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''), slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks,
		       slices.archived_at is not null, slices.description, slices.merge_on_submit
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug = $2 and slices.deleted_at is null
	`, ref.Account, ref.Slice)
	slice, err := scanSlice(row)
	if errors.Is(err, ErrNotFound) {
		// A renamed slice still answers to its earlier names.
		row = s.db.QueryRowContext(ctx, `
			select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
			       coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''), slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks,
			       slices.archived_at is not null, slices.description, slices.merge_on_submit
			from slice_aliases
			join slices on slices.id = slice_aliases.slice_id
			join accounts on accounts.id = slices.account_id
			where accounts.slug = $1 and slice_aliases.slug = $2 and slices.deleted_at is null
		`, ref.Account, ref.Slice)
		slice, err = scanSlice(row)
	}
	if err != nil {
		return nil, err
	}
	return s.withFormerNames(ctx, slice)
}

func (s *SliceStore) Get(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''), slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks,
		       slices.archived_at is not null, slices.description, slices.merge_on_submit
		from slices
		join accounts on accounts.id = slices.account_id
		where slices.id = $1
	`, sliceID)
	slice, err := scanSlice(row)
	if err != nil {
		return nil, err
	}
	return s.withFormerNames(ctx, slice)
}

func (s *SliceStore) List(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error) {
	return s.list(ctx, account, after, limit, false, "", includeArchived)
}

func (s *SliceStore) ListPublic(ctx context.Context, account, after string, limit int, includeArchived bool) ([]*corev1.Slice, error) {
	return s.list(ctx, account, after, limit, true, "", includeArchived)
}

func (s *SliceStore) ListVisible(ctx context.Context, account, subjectID, after string, limit int, includeArchived bool) ([]*corev1.Slice, error) {
	return s.list(ctx, account, after, limit, true, strings.TrimSpace(subjectID), includeArchived)
}

// list lists an account's slices: all of them, or with publicOnly the public
// ones and those shared with sharedWith. Retired slices are never listed, and
// archived ones only with includeArchived.
func (s *SliceStore) list(ctx context.Context, account, after string, limit int, publicOnly bool, sharedWith string, includeArchived bool) ([]*corev1.Slice, error) {
	account, err := normalizeSlug(account, "account")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''), slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks,
		       slices.archived_at is not null, slices.description, slices.merge_on_submit
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug > $4 and slices.deleted_at is null
		  and ($6 or slices.archived_at is null)
		  and (not $3 or slices.visibility = 'public' or ($5 <> '' and exists (
			select 1 from slice_members sm where sm.slice_id = slices.id and sm.subject_id = $5
		  )))
		order by slices.slug
		limit $2
	`, account, limit, publicOnly, after, sharedWith, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.Slice
	for rows.Next() {
		slice, err := scanSlice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, slice)
	}
	return out, rows.Err()
}

func (s *SliceStore) ListDefinitionVersions(ctx context.Context, sliceID string, limit int) ([]*corev1.SliceDefinitionVersion, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		select slice_id, version, definition_hash, visibility, included_paths,
		       required_approvals, required_checks, created_at, coalesce(created_by, '')
		from slice_definition_versions
		where slice_id = $1
		order by version desc
		limit $2
	`, sliceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.SliceDefinitionVersion
	for rows.Next() {
		version, err := scanSliceDefinitionVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, version)
	}
	return out, rows.Err()
}

func (s *SliceStore) UpdateDefinition(ctx context.Context, subjectID, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	subjectID = strings.TrimSpace(subjectID)
	sliceID = strings.TrimSpace(sliceID)
	expectedHash = strings.TrimSpace(expectedHash)
	if sliceID == "" {
		return nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if expectedHash == "" {
		return nil, fmt.Errorf("%w: expected_definition_hash is required", ErrInvalid)
	}
	if definition == nil {
		return nil, fmt.Errorf("%w: slice definition is required", ErrInvalid)
	}
	current, err := s.Get(ctx, sliceID)
	if err != nil {
		return nil, err
	}
	includedPaths, visibility, requiredApprovals, requiredChecks, err := validateSliceDefinition(current.Ref, definition.IncludedPaths, definition.Visibility, definition.RequiredApprovals, definition.RequiredChecks)
	if err != nil {
		return nil, err
	}
	included, err := encodeJSON(includedPaths)
	if err != nil {
		return nil, err
	}
	nextVersion := current.Definition.Version + 1
	requiredChecksJSON, err := encodeJSON(requiredChecks)
	if err != nil {
		return nil, err
	}
	nextHash := definitionHash(sliceID, nextVersion, includedPaths, visibility, requiredApprovals, requiredChecks)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		update slices
		set version = $1,
		    definition_hash = $2,
		    visibility = $3,
		    included_paths = $4,
		    required_approvals = $5,
		    required_checks = $6,
		    updated_at = now()
		where id = $7 and definition_hash = $8
	`, nextVersion, nextHash, visibility, included, requiredApprovals, requiredChecksJSON, sliceID, expectedHash)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrConflict
	}
	if err := appendSliceDefinitionVersionTx(ctx, tx, sliceID, nextVersion, nextHash, visibility, includedPaths, requiredApprovals, requiredChecks, subjectID); err != nil {
		return nil, err
	}
	if err := syncSliceIncludedPathsTx(ctx, tx, sliceID, includedPaths); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	slice, err := s.Get(ctx, sliceID)
	if err != nil {
		return nil, err
	}
	return slice.Definition, nil
}

func (s *SliceStore) Delete(ctx context.Context, sliceID string) error {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from slices where id = $1)`, sliceID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	var changesets int
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from changesets
		where authoring_slice_id = $1
	`, sliceID).Scan(&changesets); err != nil {
		return err
	}
	if changesets > 0 {
		return fmt.Errorf("%w: slice has changesets", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `delete from slice_aliases where slice_id = $1`, sliceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from slice_included_paths where slice_id = $1`, sliceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from secret_slices where slice_id = $1`, sliceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from slice_tags where slice_id = $1`, sliceID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `delete from slices where id = $1`, sliceID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *SliceStore) CoveringIDsByPath(ctx context.Context, changedPaths []string) (map[string][]string, error) {
	prefixes := storage.CoveragePrefixUnion(changedPaths)
	if len(prefixes) == 0 {
		return storage.AssembleCoverageByPath(changedPaths, nil), nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select prefix, slice_id
		from slice_included_paths
		where prefix = any($1)
		order by prefix, slice_id
	`, prefixes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byPrefix := map[string][]string{}
	for rows.Next() {
		var prefix string
		var sliceID string
		if err := rows.Scan(&prefix, &sliceID); err != nil {
			return nil, err
		}
		byPrefix[prefix] = append(byPrefix[prefix], sliceID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return storage.AssembleCoverageByPath(changedPaths, byPrefix), nil
}

func (s *SliceStore) requireSliceExists(ctx context.Context, sliceID string) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `select exists(select 1 from slices where id = $1)`, sliceID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func appendSliceDefinitionVersionTx(ctx context.Context, tx *sql.Tx, sliceID string, version int64, definitionHash, visibility string, includedPaths []string, requiredApprovals int32, requiredChecks []string, createdBy string) error {
	includedJSON, err := encodeJSON(includedPaths)
	if err != nil {
		return err
	}
	requiredChecksJSON, err := encodeJSON(requiredChecks)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		insert into slice_definition_versions(
			slice_id,
			version,
			definition_hash,
			visibility,
			included_paths,
			required_approvals,
			required_checks,
			created_at,
			created_by
		)
		values ($1, $2, $3, $4, $5, $6, $7, now(), nullif($8, ''))
	`, sliceID, version, definitionHash, visibility, includedJSON, requiredApprovals, requiredChecksJSON, strings.TrimSpace(createdBy))
	return err
}

func syncSliceIncludedPathsTx(ctx context.Context, tx *sql.Tx, sliceID string, prefixes []string) error {
	if _, err := tx.ExecContext(ctx, `delete from slice_included_paths where slice_id = $1`, sliceID); err != nil {
		return err
	}
	if len(prefixes) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		insert into slice_included_paths(slice_id, prefix)
		select $1, unnest($2::text[])
		on conflict do nothing
	`, sliceID, prefixes)
	return err
}

func scanSliceDefinitionVersion(row scanner) (*corev1.SliceDefinitionVersion, error) {
	var (
		out                              corev1.SliceDefinitionVersion
		requiredApprovals                int32
		includedJSON, requiredChecksJSON []byte
		createdAt                        time.Time
	)
	err := row.Scan(&out.SliceId, &out.Version, &out.DefinitionHash, &out.Visibility, &includedJSON, &requiredApprovals, &requiredChecksJSON, &createdAt, &out.CreatedBy)
	if err != nil {
		return nil, err
	}
	var included []string
	if err := decodeJSON(includedJSON, &included); err != nil {
		return nil, err
	}
	var requiredChecks []string
	if err := decodeJSON(requiredChecksJSON, &requiredChecks); err != nil {
		return nil, err
	}
	out.IncludedPaths = included
	out.RequiredApprovals = requiredApprovals
	out.RequiredChecks = requiredChecks
	out.CreatedAt = formatTime(createdAt)
	return &out, nil
}

func scanSlice(row scanner) (*corev1.Slice, error) {
	var (
		id, account, slug, definitionHash, ciDaemonID, ciDaemonName, visibility, description string
		archived, mergeOnSubmit                                                              bool
		version                                                                              int64
		requiredApprovals                                                                    int32
		includedJSON, requiredChecksJSON                                                     []byte
	)
	err := row.Scan(&id, &account, &slug, &version, &definitionHash, &ciDaemonID, &ciDaemonName, &visibility, &includedJSON, &requiredApprovals, &requiredChecksJSON, &archived, &description, &mergeOnSubmit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var included []string
	if err := decodeJSON(includedJSON, &included); err != nil {
		return nil, err
	}
	var requiredChecks []string
	if err := decodeJSON(requiredChecksJSON, &requiredChecks); err != nil {
		return nil, err
	}
	return &corev1.Slice{
		Id:             id,
		Ref:            &corev1.SliceRef{Account: account, Slice: slug},
		DefinitionHash: definitionHash,
		CiDaemonId:     ciDaemonID,
		CiDaemonName:   ciDaemonName,
		Archived:       archived,
		Description:    description,
		MergeOnSubmit:  mergeOnSubmit,
		Definition: &corev1.SliceDefinition{
			SliceId:           id,
			Version:           version,
			IncludedPaths:     included,
			Visibility:        visibility,
			RequiredApprovals: requiredApprovals,
			RequiredChecks:    requiredChecks,
		},
	}, nil
}
