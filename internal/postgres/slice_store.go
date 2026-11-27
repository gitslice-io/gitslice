package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type SliceStore struct {
	db      *sql.DB
	Secrets *secretbox.Box
}

const sliceSelectColumns = `
		slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		coalesce(slices.ci_daemon_id, ''), coalesce((select coalesce(nullif(d.label, ''), d.name) from agent_daemons d where d.id = slices.ci_daemon_id), ''),
		slices.visibility, slices.included_paths, slices.required_approvals, slices.required_human_approvals, slices.required_checks, slices.approvers_group,
		slices.archived_at is not null, slices.description, slices.merge_on_submit,
		coalesce((select json_agg(json_build_array(p.daemon_id, coalesce(nullif(d.label, ''), d.name, p.daemon_id)) order by p.position, p.added_at) from slice_ci_daemons p left join agent_daemons d on d.id = p.daemon_id where p.slice_id = slices.id), '[]'::json)`

func (s *SliceStore) Create(ctx context.Context, subjectID string, ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) (*corev1.Slice, error) {
	return s.CreateDefinition(ctx, subjectID, ref, &corev1.SliceDefinition{
		IncludedPaths:     includedPaths,
		Visibility:        visibility,
		RequiredApprovals: requiredApprovals,
		RequiredChecks:    requiredChecks,
	})
}

func (s *SliceStore) CreateDefinition(ctx context.Context, subjectID string, ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.Slice, error) {
	ref, err := storage.NormalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	subjectID = strings.TrimSpace(subjectID)
	validated, err := s.ValidateSliceDefinition(ref, definition)
	if err != nil {
		return nil, err
	}

	id, err := inTx(ctx, s.db, func(tx *sql.Tx) (string, error) {
		var accountID string
		err := tx.QueryRowContext(ctx, `select id from accounts where slug = $1`, ref.Account).Scan(&accountID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
		var existingID string
		err = tx.QueryRowContext(ctx, `
			select slices.id
			from slices
			where slices.account_id = $1 and slices.slug = $2
		`, accountID, ref.Slice).Scan(&existingID)
		if err == nil {
			return "", fmt.Errorf("%w: slice %s/%s already exists", ErrConflict, ref.Account, ref.Slice)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		var aliasOf string
		err = tx.QueryRowContext(ctx, `select slice_id from slice_aliases where account_id = $1 and slug = $2`, accountID, ref.Slice).Scan(&aliasOf)
		if err == nil {
			return "", fmt.Errorf("%w: slice name %s/%s is reserved: a slice was renamed from it", ErrConflict, ref.Account, ref.Slice)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}

		id, err := objectid.RandomID("slice")
		if err != nil {
			return "", err
		}
		includedJSON, err := encodeJSON(validated.IncludedPaths)
		if err != nil {
			return "", err
		}
		requiredChecksJSON, err := encodeJSON(validated.RequiredChecks)
		if err != nil {
			return "", err
		}
		definitionHash := definitionHashWithApprovers(id, 1, validated.IncludedPaths, validated.Visibility, validated.RequiredApprovals, validated.RequiredHumanApprovals, validated.RequiredChecks, validated.Approvers)
		_, err = tx.ExecContext(ctx, `
			insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, required_approvals, required_human_approvals, required_checks, approvers_group, created_at, updated_at)
			values ($1, $2, $3, 1, $4, $5, $6, $7, $8, $9, $10, now(), now())
		`, id, accountID, ref.Slice, definitionHash, validated.Visibility, includedJSON, validated.RequiredApprovals, validated.RequiredHumanApprovals, requiredChecksJSON, validated.Approvers)
		if err != nil {
			return "", err
		}
		if err := appendSliceDefinitionVersionTx(ctx, tx, id, 1, definitionHash, validated.Visibility, validated.IncludedPaths, validated.RequiredApprovals, validated.RequiredHumanApprovals, validated.RequiredChecks, validated.Approvers, subjectID); err != nil {
			return "", err
		}
		if err := syncSliceIncludedPathsTx(ctx, tx, id, validated.IncludedPaths); err != nil {
			return "", err
		}
		return id, nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *SliceStore) ValidateSliceDefinition(ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	return storage.ValidateSliceDefinition(ref, definition)
}

func (s *SliceStore) Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	ref, err := storage.NormalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		select `+sliceSelectColumns+`
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug = $2 and slices.deleted_at is null
	`, ref.Account, ref.Slice)
	slice, err := scanSlice(row)
	if errors.Is(err, ErrNotFound) {
		// A renamed slice still answers to its earlier names.
		row = s.db.QueryRowContext(ctx, `
			select `+sliceSelectColumns+`
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
		select `+sliceSelectColumns+`
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

// list lists an account's slices: all of them, or with publicOnly the public
// ones and those shared with sharedWith. Retired slices are never listed, and
// archived ones only with includeArchived.
func (s *SliceStore) list(ctx context.Context, account, after string, limit int, publicOnly bool, sharedWith string, includeArchived bool) ([]*corev1.Slice, error) {
	account, err := storage.NormalizeSlug(account, "account")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		select `+sliceSelectColumns+`,
		       slices.created_at,
		       coalesce((select max(c.updated_at) from changesets c
		                 where c.authoring_slice_id = slices.id and c.status = 'submitted'), slices.created_at)
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug > $4 and slices.deleted_at is null
		  and ($6 or slices.archived_at is null)
		  and (not $3 or slices.visibility = 'public' or ($5 <> '' and (
			exists (select 1 from slice_members sm where sm.slice_id = slices.id and sm.subject_id = $5)
			or exists (
				select 1
				from slice_group_members sgm
				join account_group_members agm on agm.group_id = sgm.group_id
				where sgm.slice_id = slices.id and agm.subject_id = $5
			)
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
		var createdAt, activeAt time.Time
		slice, err := scanSlice(timesScanner{row: rows, times: []*time.Time{&createdAt, &activeAt}})
		if err != nil {
			return nil, err
		}
		slice.CreatedAt = formatTime(createdAt)
		slice.LastActivityAt = formatTime(activeAt)
		out = append(out, slice)
	}
	return out, rows.Err()
}

// timesScanner lets scanSlice read a slice row that has timestamps as more
// columns.
type timesScanner struct {
	row   scanner
	times []*time.Time
}

func (t timesScanner) Scan(dest ...any) error {
	for _, at := range t.times {
		dest = append(dest, at)
	}
	return t.row.Scan(dest...)
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
		       required_approvals, required_human_approvals, required_checks, approvers_group, created_at, coalesce(created_by, '')
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
	validated, err := storage.ValidateSliceDefinition(current.Ref, definition)
	if err != nil {
		return nil, err
	}
	included, err := encodeJSON(validated.IncludedPaths)
	if err != nil {
		return nil, err
	}
	nextVersion := current.Definition.Version + 1
	requiredChecksJSON, err := encodeJSON(validated.RequiredChecks)
	if err != nil {
		return nil, err
	}
	nextHash := definitionHashWithApprovers(sliceID, nextVersion, validated.IncludedPaths, validated.Visibility, validated.RequiredApprovals, validated.RequiredHumanApprovals, validated.RequiredChecks, validated.Approvers)
	if err := execInTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			update slices
			set version = $1,
			    definition_hash = $2,
			    visibility = $3,
			    included_paths = $4,
			    required_approvals = $5,
			    required_human_approvals = $6,
			    required_checks = $7,
			    approvers_group = $8,
			    updated_at = now()
			where id = $9 and definition_hash = $10
		`, nextVersion, nextHash, validated.Visibility, included, validated.RequiredApprovals, validated.RequiredHumanApprovals, requiredChecksJSON, validated.Approvers, sliceID, expectedHash)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrConflict
		}
		if err := appendSliceDefinitionVersionTx(ctx, tx, sliceID, nextVersion, nextHash, validated.Visibility, validated.IncludedPaths, validated.RequiredApprovals, validated.RequiredHumanApprovals, validated.RequiredChecks, validated.Approvers, subjectID); err != nil {
			return err
		}
		return syncSliceIncludedPathsTx(ctx, tx, sliceID, validated.IncludedPaths)
	}); err != nil {
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
	return execInTx(ctx, s.db, func(tx *sql.Tx) error {
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
		return nil
	})
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

func appendSliceDefinitionVersionTx(ctx context.Context, tx *sql.Tx, sliceID string, version int64, definitionHash, visibility string, includedPaths []string, requiredApprovals, requiredHumanApprovals int32, requiredChecks []string, approvers, createdBy string) error {
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
			required_human_approvals,
			required_checks,
			approvers_group,
			created_at,
			created_by
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), nullif($10, ''))
	`, sliceID, version, definitionHash, visibility, includedJSON, requiredApprovals, requiredHumanApprovals, requiredChecksJSON, strings.TrimSpace(approvers), strings.TrimSpace(createdBy))
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
		out                                       corev1.SliceDefinitionVersion
		requiredApprovals, requiredHumanApprovals int32
		includedJSON, requiredChecksJSON          []byte
		createdAt                                 time.Time
	)
	err := row.Scan(&out.SliceId, &out.Version, &out.DefinitionHash, &out.Visibility, &includedJSON, &requiredApprovals, &requiredHumanApprovals, &requiredChecksJSON, &out.Approvers, &createdAt, &out.CreatedBy)
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
	out.RequiredHumanApprovals = requiredHumanApprovals
	out.RequiredChecks = requiredChecks
	out.CreatedAt = formatTime(createdAt)
	return &out, nil
}

func scanSlice(row scanner) (*corev1.Slice, error) {
	var (
		id, account, slug, definitionHash, ciDaemonID, ciDaemonName, visibility, approvers, description string
		archived, mergeOnSubmit                                                                         bool
		version                                                                                         int64
		requiredApprovals, requiredHumanApprovals                                                       int32
		includedJSON, requiredChecksJSON, poolJSON                                                      []byte
	)
	err := row.Scan(&id, &account, &slug, &version, &definitionHash, &ciDaemonID, &ciDaemonName, &visibility, &includedJSON, &requiredApprovals, &requiredHumanApprovals, &requiredChecksJSON, &approvers, &archived, &description, &mergeOnSubmit, &poolJSON)
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
	var pool [][2]string
	if err := decodeJSON(poolJSON, &pool); err != nil {
		return nil, err
	}
	daemonIDs := make([]string, 0, len(pool))
	daemonNames := make([]string, 0, len(pool))
	for _, member := range pool {
		daemonIDs = append(daemonIDs, member[0])
		daemonNames = append(daemonNames, member[1])
	}
	return &corev1.Slice{
		CiDaemonIds:    daemonIDs,
		CiDaemonNames:  daemonNames,
		Id:             id,
		Ref:            &corev1.SliceRef{Account: account, Slice: slug},
		DefinitionHash: definitionHash,
		CiDaemonId:     ciDaemonID,
		CiDaemonName:   ciDaemonName,
		Archived:       archived,
		Description:    description,
		MergeOnSubmit:  mergeOnSubmit,
		Definition: &corev1.SliceDefinition{
			SliceId:                id,
			Version:                version,
			IncludedPaths:          included,
			Visibility:             visibility,
			RequiredApprovals:      requiredApprovals,
			RequiredHumanApprovals: requiredHumanApprovals,
			RequiredChecks:         requiredChecks,
			Approvers:              approvers,
		},
	}, nil
}
