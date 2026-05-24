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
