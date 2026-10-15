package postgres

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"path/filepath"
	"strings"
)

type materializedPathEntry struct {
	Path        string
	Kind        string
	ContentHash string
	Mode        uint32
	BlobID      string
	Size        int64
	BlobOK      bool
}

// materializedHeadCommit returns the commit id that current_path_entities
// currently reflects for targetRef, or ("", false) if none is recorded.
func (s *RepositoryStore) materializedHeadCommit(ctx context.Context, targetRef string) (string, bool, error) {
	if targetRef == "" {
		targetRef = DefaultTargetRef
	}
	var commitID string
	err := s.db.QueryRowContext(ctx, `
		select commit_id
		from ref_materialized_heads
		where target_ref = $1
	`, targetRef).Scan(&commitID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return commitID, true, nil
}

func (s *RepositoryStore) getFileFromMaterializedHead(ctx context.Context, targetRef, commitID, p string) (*FileEntry, bool, error) {
	if s.trees == nil {
		return nil, false, nil
	}
	lookupPath, ok := flatIndexLookupPath(p)
	if !ok || lookupPath == "/" {
		return nil, false, nil
	}
	entry, ok, handled, err := s.materializedPathEntryAt(ctx, targetRef, commitID, lookupPath)
	if err != nil || !handled {
		return nil, false, nil
	}
	if !ok || entry.Kind != "file" {
		return nil, true, ErrNotFound
	}
	if !entry.completeFile() {
		return nil, false, nil
	}
	return &FileEntry{
		Path:        entry.Path,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	}, true, nil
}

func (s *RepositoryStore) getEntryFromMaterializedHead(ctx context.Context, targetRef, commitID, p string) (*TreeEntry, bool, error) {
	if s.trees == nil {
		return nil, false, nil
	}
	lookupPath, ok := flatIndexLookupPath(p)
	if !ok || lookupPath == "/" {
		return nil, false, nil
	}
	entry, ok, handled, err := s.materializedPathEntryAt(ctx, targetRef, commitID, lookupPath)
	if err != nil || !handled || !ok {
		return nil, false, nil
	}
	if entry.Kind != "file" {
		return nil, false, nil
	}
	if !entry.completeFile() {
		return nil, false, nil
	}
	return &TreeEntry{
		Path:        entry.Path,
		Name:        path.Base(entry.Path),
		Kind:        "file",
		Mode:        entry.Mode,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Size:        entry.Size,
	}, true, nil
}

// materializedPathEntryAt reads p from the flat index (current_path_entities)
// when the index reflects commitID; handled is false when it does not. The
// marker and the entry are read in one statement, so one snapshot: read
// separately, a commit published between the two reads could be served as
// commitID's.
func (s *RepositoryStore) materializedPathEntryAt(ctx context.Context, targetRef, commitID, p string) (entry materializedPathEntry, found, handled bool, err error) {
	if targetRef == "" {
		targetRef = DefaultTargetRef
	}
	var markerCommitID string
	var entryPath, kind, contentHash, blobID sql.NullString
	var mode, size sql.NullInt64
	err = s.db.QueryRowContext(ctx, `
		select h.commit_id, cpe.path, cpe.kind, cpe.content_hash, cpe.mode, b.id, b.size
		from ref_materialized_heads h
		left join current_path_entities cpe on cpe.target_ref = h.target_ref and cpe.path = $2
		left join blobs b on b.content_hash = cpe.content_hash
		where h.target_ref = $1
	`, targetRef, p).Scan(&markerCommitID, &entryPath, &kind, &contentHash, &mode, &blobID, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return materializedPathEntry{}, false, false, nil
	}
	if err != nil {
		return materializedPathEntry{}, false, false, err
	}
	if markerCommitID != commitID {
		return materializedPathEntry{}, false, false, nil
	}
	if !entryPath.Valid {
		return materializedPathEntry{}, false, true, nil
	}
	entry.Path, entry.Kind = entryPath.String, kind.String
	entry.ContentHash = contentHash.String
	if mode.Valid && mode.Int64 > 0 {
		entry.Mode = uint32(mode.Int64)
	}
	if blobID.Valid && size.Valid {
		entry.BlobID = blobID.String
		entry.Size = size.Int64
		entry.BlobOK = true
	}
	return entry, true, true, nil
}

func (entry materializedPathEntry) completeFile() bool {
	return entry.Kind == "file" && entry.ContentHash != "" && entry.BlobOK && entry.BlobID != ""
}

func flatIndexLookupPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "/", true
	}
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	cleaned := filepath.ToSlash(filepath.Clean(p))
	if cleaned == "/" {
		return "/", true
	}
	trimmed := strings.TrimPrefix(cleaned, "/")
	if strings.HasPrefix(trimmed, "../") || trimmed == ".." {
		return "", false
	}
	return cleaned, true
}
