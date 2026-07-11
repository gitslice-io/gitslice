package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gitslice-io/gitslice/proto/core/v1"
)

type BlobStore struct {
	db *sql.DB
}

func (s *BlobStore) Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error {
	_, err := s.db.ExecContext(ctx, `
		insert into blobs(id, content_hash, size, storage_location, state)
		values ($1, $2, $3, $4, 'available')
		on conflict (id) do update
		set content_hash = excluded.content_hash,
		    size = excluded.size,
		    storage_location = excluded.storage_location,
		    state = excluded.state
	`, blobID, contentHash, size, storageLocation)
	return err
}

func (s *BlobStore) GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error) {
	var blob corev1.BlobRecord
	err := s.db.QueryRowContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where id = $1
	`, blobID).Scan(&blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &blob, nil
}

func (s *BlobStore) GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where content_hash = any($1)
		order by content_hash
	`, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.BlobRecord
	for rows.Next() {
		var blob corev1.BlobRecord
		if err := rows.Scan(&blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State); err != nil {
			return nil, err
		}
		out = append(out, &blob)
	}
	return out, rows.Err()
}

// AssociateSlices records that the content hashes are referenced by the
// slice. It is idempotent, and empty input is a no-op.
func (s *BlobStore) AssociateSlices(ctx context.Context, sliceID string, contentHashes []string) error {
	if len(contentHashes) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		insert into blob_slices(content_hash, slice_id)
		select distinct content_hash, $1
		from unnest($2::text[]) as content_hash
		on conflict (content_hash, slice_id) do nothing
	`, sliceID, contentHashes)
	return err
}

// SliceAssociations reports which content hashes are associated with the
// slice in blob_slices.
func (s *BlobStore) SliceAssociations(ctx context.Context, sliceID string, contentHashes []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(contentHashes) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select content_hash
		from blob_slices
		where slice_id = $1 and content_hash = any($2)
	`, sliceID, contentHashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var contentHash string
		if err := rows.Scan(&contentHash); err != nil {
			return nil, err
		}
		out[contentHash] = true
	}
	return out, rows.Err()
}

// PathsByContentHash returns the path_heads paths currently recording each
// content hash, keyed by hash.
func (s *BlobStore) PathsByContentHash(ctx context.Context, contentHashes []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(contentHashes) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select content_hash, path
		from path_heads
		where content_hash = any($1)
		order by content_hash, path
	`, contentHashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var contentHash, path string
		if err := rows.Scan(&contentHash, &path); err != nil {
			return nil, err
		}
		out[contentHash] = append(out[contentHash], path)
	}
	return out, rows.Err()
}

func getBlobTx(ctx context.Context, tx *sql.Tx, blobID string) (*corev1.BlobRecord, error) {
	var blob corev1.BlobRecord
	err := tx.QueryRowContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where id = $1 and state = 'available'
	`, blobID).Scan(&blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &blob, nil
}
