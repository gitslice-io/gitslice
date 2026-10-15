package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
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

// UpsertMany is Upsert for many blobs in one statement. Rows are deduplicated
// and sorted by id, so concurrent batches lock rows in the same order.
func (s *BlobStore) UpsertMany(ctx context.Context, blobs []storage.BlobUpsert) error {
	byID := make(map[string]storage.BlobUpsert, len(blobs))
	for _, blob := range blobs {
		byID[blob.ID] = blob
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	hashes := make([]string, len(ids))
	sizes := make([]int64, len(ids))
	locations := make([]string, len(ids))
	for i, id := range ids {
		hashes[i], sizes[i], locations[i] = byID[id].ContentHash, byID[id].Size, byID[id].StorageLocation
	}
	_, err := s.db.ExecContext(ctx, `
		insert into blobs(id, content_hash, size, storage_location, state)
		select id, content_hash, size, storage_location, 'available'
		from unnest($1::text[], $2::text[], $3::bigint[], $4::text[]) as b(id, content_hash, size, storage_location)
		on conflict (id) do update
		set content_hash = excluded.content_hash,
		    size = excluded.size,
		    storage_location = excluded.storage_location,
		    state = excluded.state
	`, ids, hashes, sizes, locations)
	return err
}

func (s *BlobStore) GetByIDs(ctx context.Context, blobIDs []string) (map[string]*corev1.BlobRecord, error) {
	out := map[string]*corev1.BlobRecord{}
	if len(blobIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where id = any($1)
	`, blobIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var blob corev1.BlobRecord
		if err := rows.Scan(&blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State); err != nil {
			return nil, err
		}
		out[blob.Id] = &blob
	}
	return out, rows.Err()
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

func (s *BlobStore) SetGitBlobIDs(ctx context.Context, ids map[string]string) error {
	if len(ids) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(ids))
	for contentHash := range ids {
		hashes = append(hashes, contentHash)
	}
	sort.Strings(hashes)
	gitIDs := make([]string, len(hashes))
	for i, contentHash := range hashes {
		gitIDs[i] = ids[contentHash]
	}
	_, err := s.db.ExecContext(ctx, `
		update blobs set git_blob_id = v.git_blob_id
		from unnest($1::text[], $2::text[]) as v(content_hash, git_blob_id)
		where blobs.content_hash = v.content_hash
	`, hashes, gitIDs)
	return err
}

func (s *BlobStore) GitBlobIDs(ctx context.Context, contentHashes []string) (map[string]string, error) {
	out := map[string]string{}
	if len(contentHashes) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select content_hash, git_blob_id from blobs
		where content_hash = any($1) and git_blob_id is not null
	`, contentHashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var contentHash, gitID string
		if err := rows.Scan(&contentHash, &gitID); err != nil {
			return nil, err
		}
		out[contentHash] = gitID
	}
	return out, rows.Err()
}

func (s *BlobStore) ListMissingGitBlobIDs(ctx context.Context, limit int) ([]*corev1.BlobRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, content_hash, size, storage_location, state
		from blobs
		where git_blob_id is null and state = 'available'
		order by created_at, id
		limit $1
	`, limit)
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

func (s *BlobStore) BlobsByGitIDs(ctx context.Context, gitBlobIDs []string) (map[string]*corev1.BlobRecord, error) {
	out := map[string]*corev1.BlobRecord{}
	if len(gitBlobIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select git_blob_id, id, content_hash, size, storage_location, state
		from blobs
		where git_blob_id = any($1)
	`, gitBlobIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var gitID string
		var blob corev1.BlobRecord
		if err := rows.Scan(&gitID, &blob.Id, &blob.ContentHash, &blob.Size, &blob.StorageLocation, &blob.State); err != nil {
			return nil, err
		}
		out[gitID] = &blob
	}
	return out, rows.Err()
}
