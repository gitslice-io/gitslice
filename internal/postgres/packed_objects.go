package postgres

import (
	"context"
	"database/sql"
	"errors"

	"gitslice.io/gitslice/internal/objectstore/packed"
)

const packedObjectInsertChunkSize = 5000

// PackedObjectStore persists the packed_objects byte-range index in Postgres.
type PackedObjectStore struct {
	db *sql.DB
}

func (s *PackedObjectStore) Lookup(ctx context.Context, objectKey string) (packed.Location, bool, error) {
	var loc packed.Location
	err := s.db.QueryRowContext(ctx, `
		select pack_key, byte_offset, byte_length, codec, raw_length
		from packed_objects
		where object_key = $1
	`, objectKey).Scan(&loc.PackKey, &loc.Offset, &loc.Length, &loc.Codec, &loc.RawLength)
	if errors.Is(err, sql.ErrNoRows) {
		return packed.Location{}, false, nil
	}
	if err != nil {
		return packed.Location{}, false, err
	}
	return loc, true, nil
}

func (s *PackedObjectStore) RecordPack(ctx context.Context, packKey string, entries []packed.IndexedEntry) error {
	if len(entries) == 0 {
		return nil
	}
	for start := 0; start < len(entries); start += packedObjectInsertChunkSize {
		end := min(start+packedObjectInsertChunkSize, len(entries))
		chunk := entries[start:end]
		keys := make([]string, len(chunk))
		offsets := make([]int64, len(chunk))
		lengths := make([]int64, len(chunk))
		codecs := make([]string, len(chunk))
		rawLengths := make([]int64, len(chunk))
		for i, entry := range chunk {
			keys[i] = entry.ObjectKey
			offsets[i] = entry.Offset
			lengths[i] = entry.Length
			codecs[i] = entry.Codec
			rawLengths[i] = entry.RawLength
		}
		if _, err := s.db.ExecContext(ctx, `
			insert into packed_objects(object_key, pack_key, byte_offset, byte_length, codec, raw_length)
			select input.object_key, $1, input.byte_offset, input.byte_length, input.codec, input.raw_length
			from unnest($2::text[], $3::bigint[], $4::bigint[], $5::text[], $6::bigint[])
				as input(object_key, byte_offset, byte_length, codec, raw_length)
			on conflict (object_key) do nothing
		`, packKey, keys, offsets, lengths, codecs, rawLengths); err != nil {
			return err
		}
	}
	return nil
}

func (s *PackedObjectStore) DeleteKey(ctx context.Context, objectKey string) error {
	_, err := s.db.ExecContext(ctx, `delete from packed_objects where object_key = $1`, objectKey)
	return err
}
