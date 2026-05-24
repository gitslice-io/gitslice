package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type SliceStore struct {
	db *sql.DB
}

func (s *SliceStore) Resolve(ctx context.Context, ref *corev1.SliceRef) (*corev1.Slice, error) {
	if ref == nil || ref.Account == "" || ref.Slice == "" {
		return nil, fmt.Errorf("slice ref requires account and slice")
	}
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths
		from slices
		join accounts on accounts.id = slices.account_id
		where accounts.slug = $1 and slices.slug = $2
	`, ref.Account, ref.Slice)
	return scanSlice(row)
}

func (s *SliceStore) Get(ctx context.Context, sliceID string) (*corev1.Slice, error) {
	row := s.db.QueryRowContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths
		from slices
		join accounts on accounts.id = slices.account_id
		where slices.id = $1
	`, sliceID)
	return scanSlice(row)
}

func (s *SliceStore) List(ctx context.Context, account string, limit int) ([]*corev1.Slice, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		select slices.id, accounts.slug, slices.slug, slices.version, slices.definition_hash,
		       slices.visibility, slices.included_paths
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
	if definition == nil {
		return nil, fmt.Errorf("slice definition is required")
	}
	included, err := encodeJSON(definition.IncludedPaths)
	if err != nil {
		return nil, err
	}
	nextHash := definitionHash(sliceID, definition.Version+1, definition.IncludedPaths, definition.Visibility)
	res, err := s.db.ExecContext(ctx, `
		update slices
		set version = version + 1,
		    definition_hash = $1,
		    visibility = $2,
		    included_paths = $3,
		    updated_at = now()
		where id = $4 and definition_hash = $5
	`, nextHash, definition.Visibility, included, sliceID, expectedHash)
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

func scanSlice(row scanner) (*corev1.Slice, error) {
	var (
		id, account, slug, definitionHash, visibility string
		version                                       int64
		includedJSON                                  []byte
	)
	err := row.Scan(&id, &account, &slug, &version, &definitionHash, &visibility, &includedJSON)
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
	return &corev1.Slice{
		Id:             id,
		Ref:            &corev1.SliceRef{Account: account, Slice: slug},
		DefinitionHash: definitionHash,
		Definition: &corev1.SliceDefinition{
			SliceId:       id,
			Version:       version,
			IncludedPaths: included,
			Visibility:    visibility,
		},
	}, nil
}
