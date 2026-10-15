package postgres

import (
	"context"
	"database/sql"
	"errors"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) validateAcceptedPathBasesTx(ctx context.Context, tx *sql.Tx, targetRef, currentCommitID string, bases []*corev1.PathBase) error {
	paths := make([]string, 0, len(bases))
	for _, base := range bases {
		paths = append(paths, base.Path)
	}
	paths = uniqueSortedStrings(paths)
	heads, err := getPathHeadsTx(ctx, tx, paths)
	if err != nil {
		return err
	}
	missingPaths := make([]string, 0, len(paths)-len(heads))
	for _, p := range paths {
		if _, ok := heads[p]; !ok {
			missingPaths = append(missingPaths, p)
		}
	}
	if len(missingPaths) > 0 {
		initialHeads, err := s.initialAcceptedPathHeadsTx(ctx, tx, targetRef, currentCommitID, missingPaths)
		if err != nil {
			return err
		}
		if err := insertInitialPathHeadsTx(ctx, tx, initialHeads); err != nil {
			return err
		}
		initialized, err := getPathHeadsTx(ctx, tx, missingPaths)
		if err != nil {
			return err
		}
		for p, head := range initialized {
			heads[p] = head
		}
	}
	for _, base := range bases {
		head, ok := heads[base.Path]
		if !ok {
			return ErrNotFound
		}
		if err := validateAcceptedPathBase(head, base); err != nil {
			return err
		}
	}
	return nil
}

func (s *ChangesetStore) initialAcceptedPathHeadsTx(ctx context.Context, tx *sql.Tx, targetRef, currentCommitID string, paths []string) ([]PathHead, error) {
	if targetRef == "" {
		targetRef = DefaultTargetRef
	}
	heads, handled, err := s.initialAcceptedPathHeadsFromCurrentEntitiesTx(ctx, tx, targetRef, currentCommitID, paths)
	if err != nil || handled {
		return heads, err
	}
	return s.initialAcceptedPathHeadsFromTreeTx(ctx, tx, currentCommitID, paths)
}

// initialAcceptedPathHeadsFromCurrentEntitiesTx reads the paths' heads from
// the flat index when it reflects currentCommitID; handled is false when it
// does not. The marker and the entries are read in one statement, so one
// snapshot (the transaction is read committed): read separately, entries of a
// commit published between the two reads could be taken as currentCommitID's.
func (s *ChangesetStore) initialAcceptedPathHeadsFromCurrentEntitiesTx(ctx context.Context, tx *sql.Tx, targetRef, currentCommitID string, paths []string) ([]PathHead, bool, error) {
	type currentHeadRow struct {
		path        string
		kind        string
		contentHash string
		mode        int64
		blobID      string
		size        int64
	}
	rows, err := tx.QueryContext(ctx, `
		select h.commit_id,
		       cpe.path,
		       coalesce(cpe.kind, ''),
		       coalesce(cpe.content_hash, ''),
		       coalesce(cpe.mode, 0),
		       coalesce(b.id, ''),
		       coalesce(b.size, 0)
		from ref_materialized_heads h
		left join current_path_entities cpe on cpe.target_ref = h.target_ref and cpe.path = any($2)
		left join blobs b on b.content_hash = cpe.content_hash
		where h.target_ref = $1
	`, targetRef, paths)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	marked := false
	found := make(map[string]struct{}, len(paths))
	fallbackPaths := make([]string, 0)
	heads := make([]PathHead, 0, len(paths))
	for rows.Next() {
		var row currentHeadRow
		var markerCommitID string
		var entryPath sql.NullString
		if err := rows.Scan(&markerCommitID, &entryPath, &row.kind, &row.contentHash, &row.mode, &row.blobID, &row.size); err != nil {
			return nil, false, err
		}
		if markerCommitID != currentCommitID {
			return nil, false, nil
		}
		marked = true
		if !entryPath.Valid {
			continue
		}
		row.path = entryPath.String
		found[row.path] = struct{}{}
		if row.kind != "file" || row.contentHash == "" || row.blobID == "" || row.mode <= 0 {
			fallbackPaths = append(fallbackPaths, row.path)
			continue
		}
		heads = append(heads, pathHeadFromFile(FileEntry{
			Path:        row.path,
			BlobID:      row.blobID,
			ContentHash: row.contentHash,
			Mode:        uint32(row.mode),
			Size:        row.size,
		}))
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if !marked {
		return nil, false, nil
	}
	for _, p := range paths {
		if _, ok := found[p]; ok {
			continue
		}
		heads = append(heads, PathHead{
			Path:             p,
			Exists:           false,
			EntryFingerprint: MissingEntryFingerprint(),
		})
	}
	if len(fallbackPaths) > 0 {
		fallbackHeads, err := s.initialAcceptedPathHeadsFromTreeTx(ctx, tx, currentCommitID, fallbackPaths)
		if err != nil {
			return nil, false, err
		}
		heads = append(heads, fallbackHeads...)
	}
	return heads, true, nil
}

func (s *ChangesetStore) initialAcceptedPathHeadsFromTreeTx(ctx context.Context, tx *sql.Tx, currentCommitID string, paths []string) ([]PathHead, error) {
	rootTreeID, err := rootTreeIDForCommitTx(ctx, tx, currentCommitID)
	if err != nil {
		return nil, err
	}
	initialHeads := make([]PathHead, 0, len(paths))
	for _, p := range paths {
		entry, err := s.repository.getEntryFromTree(ctx, rootTreeID, p)
		if errors.Is(err, ErrNotFound) {
			initialHeads = append(initialHeads, PathHead{
				Path:             p,
				Exists:           false,
				EntryFingerprint: MissingEntryFingerprint(),
			})
			continue
		}
		if err != nil {
			return nil, err
		}
		initialHeads = append(initialHeads, pathHeadFromTreeEntry(*entry))
	}
	return initialHeads, nil
}

func validateAcceptedPathBase(head PathHead, base *corev1.PathBase) error {
	if !head.Exists {
		if base.Exists || base.EntryFingerprint != MissingEntryFingerprint() {
			return ErrConflict
		}
		return nil
	}
	if !base.Exists {
		return ErrConflict
	}
	if head.EntryFingerprint != base.EntryFingerprint {
		return ErrConflict
	}
	return nil
}

func getPathHeadsTx(ctx context.Context, tx *sql.Tx, paths []string) (map[string]PathHead, error) {
	heads := make(map[string]PathHead, len(paths))
	if len(paths) == 0 {
		return heads, nil
	}
	rows, err := tx.QueryContext(ctx, `
		select path, exists, entry_fingerprint, blob_id, content_hash, mode, size
		from path_heads
		where path = any($1)
		order by path
		for update
	`, paths)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		head, err := scanPathHead(rows)
		if err != nil {
			return nil, err
		}
		heads[head.Path] = head
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return heads, nil
}
