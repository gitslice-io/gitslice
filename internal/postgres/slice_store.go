package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/gitslice-io/gitslice/internal/paths"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type SliceStore struct {
	db *sql.DB
}

func (s *SliceStore) Create(ctx context.Context, ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) (*corev1.Slice, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	includedPaths, visibility, requiredApprovals, requiredChecks, err = s.ValidateDefinition(ref, includedPaths, visibility, requiredApprovals, requiredChecks)
	if err != nil {
		return nil, err
	}

	var accountID string
	err = s.db.QueryRowContext(ctx, `select id from accounts where slug = $1`, ref.Account).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var existingID string
	err = s.db.QueryRowContext(ctx, `
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
	_, err = s.db.ExecContext(ctx, `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, required_approvals, required_checks, created_at, updated_at)
		values ($1, $2, $3, 1, $4, $5, $6, $7, $8, now(), now())
	`, id, accountID, ref.Slice, definitionHash, visibility, includedJSON, requiredApprovals, requiredChecksJSON)
	if err != nil {
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
		       slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug = $2
	`, ref.Account, ref.Slice)
	return scanSlice(row)
}

func (s *SliceStore) Get(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks
		from slices
		join accounts on accounts.id = slices.account_id
		where slices.id = $1
	`, sliceID)
	return scanSlice(row)
}

func (s *SliceStore) List(ctx context.Context, account string, limit int) ([]*corev1.Slice, error) {
	account, err := normalizeSlug(account, "account")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths, slices.required_approvals, slices.required_checks
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1
		order by slices.slug
		limit $2
	`, account, limit)
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

func (s *SliceStore) UpdateDefinition(ctx context.Context, sliceID, expectedHash string, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
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
	res, err := s.db.ExecContext(ctx, `
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
	if _, err := s.Get(ctx, sliceID); err != nil {
		return err
	}
	var changesets int
	if err := s.db.QueryRowContext(ctx, `
		select count(*)
		from changesets
		where authoring_slice_id = $1
	`, sliceID).Scan(&changesets); err != nil {
		return err
	}
	if changesets > 0 {
		return fmt.Errorf("%w: slice has changesets", ErrConflict)
	}
	res, err := s.db.ExecContext(ctx, `delete from slices where id = $1`, sliceID)
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
}

func (s *SliceStore) CoveringIDs(ctx context.Context, p string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `select id, included_paths from slices order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var prefixes []string
		if err := decodeJSON(raw, &prefixes); err != nil {
			return nil, err
		}
		for _, prefix := range prefixes {
			if p == strings.TrimRight(prefix, "/") || strings.HasPrefix(p, strings.TrimRight(prefix, "/")+"/") {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids, rows.Err()
}

func normalizeSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	if ref == nil {
		return nil, fmt.Errorf("%w: slice ref is required", ErrInvalid)
	}
	account, err := normalizeSlug(ref.Account, "account")
	if err != nil {
		return nil, err
	}
	slug, err := normalizeSlug(ref.Slice, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

func normalizeSlug(value, name string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	if len(value) > 63 {
		return "", fmt.Errorf("%w: %s must be 63 characters or fewer", ErrInvalid, name)
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", fmt.Errorf("%w: %s must not start or end with '-'", ErrInvalid, name)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("%w: %s may contain only letters, numbers, '-' or '_'", ErrInvalid, name)
	}
	return value, nil
}

func validateSliceDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error) {
	if ref == nil {
		return nil, "", 0, nil, fmt.Errorf("%w: slice ref is required", ErrInvalid)
	}
	visibility = strings.TrimSpace(visibility)
	if visibility == "" {
		visibility = "account"
	}
	switch visibility {
	case "private", "account", "public":
	default:
		return nil, "", 0, nil, fmt.Errorf("%w: visibility must be private, account, or public", ErrInvalid)
	}
	if len(includedPaths) == 0 {
		return nil, "", 0, nil, fmt.Errorf("%w: included path is required", ErrInvalid)
	}
	out := make([]string, 0, len(includedPaths))
	seen := map[string]struct{}{}
	for _, raw := range includedPaths {
		cleaned, err := canonicalSliceIncludedPath(ref, raw)
		if err != nil {
			return nil, "", 0, nil, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	normalizedApprovals, normalizedChecks, err := validateSubmitSettings(requiredApprovals, requiredChecks)
	if err != nil {
		return nil, "", 0, nil, err
	}
	return out, visibility, normalizedApprovals, normalizedChecks, nil
}

func validateSubmitSettings(requiredApprovals int32, requiredChecks []string) (int32, []string, error) {
	if requiredApprovals < 0 {
		return 0, nil, fmt.Errorf("%w: required approvals must be zero or greater", ErrInvalid)
	}
	checks := make([]string, 0, len(requiredChecks))
	seen := map[string]struct{}{}
	for _, raw := range requiredChecks {
		check := strings.TrimSpace(raw)
		if check == "" {
			return 0, nil, fmt.Errorf("%w: required check name is required", ErrInvalid)
		}
		if strings.Contains(check, ",") {
			return 0, nil, fmt.Errorf("%w: required check %q must not contain commas; pass each check separately", ErrInvalid, check)
		}
		if _, ok := seen[check]; ok {
			continue
		}
		seen[check] = struct{}{}
		checks = append(checks, check)
	}
	return requiredApprovals, checks, nil
}

func canonicalSliceIncludedPath(ref *corev1.SliceRef, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: included path is required", ErrInvalid)
	}
	if strings.Contains(value, ",") {
		return "", fmt.Errorf("%w: included path %q must not contain commas; pass each included path separately", ErrInvalid, value)
	}
	cleaned, err := paths.Canonical(value)
	if err != nil {
		accountRoot, rootErr := canonicalAccountRootPath(value)
		if rootErr != nil || ref.Slice != "home" {
			return "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		cleaned = accountRoot
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) == 0 || segments[0] != ref.Account {
		return "", fmt.Errorf("%w: included path %s must be under /%s", ErrInvalid, cleaned, ref.Account)
	}
	if len(segments) == 1 && ref.Slice != "home" {
		return "", fmt.Errorf("%w: only home slices may include account root %s", ErrInvalid, cleaned)
	}
	return cleaned, nil
}

func canonicalAccountRootPath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("path must include account segment")
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) != 1 {
		return "", fmt.Errorf("path is not an account root")
	}
	if segments[0] == "" || segments[0] == "." || segments[0] == ".." {
		return "", fmt.Errorf("invalid account path segment")
	}
	return cleaned, nil
}

func scanSlice(row scanner) (*corev1.Slice, error) {
	var (
		id, account, slug, definitionHash, visibility string
		version                                       int64
		requiredApprovals                             int32
		includedJSON, requiredChecksJSON              []byte
	)
	err := row.Scan(&id, &account, &slug, &version, &definitionHash, &visibility, &includedJSON, &requiredApprovals, &requiredChecksJSON)
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
