package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gitslice.io/gitslice/internal/treestore"
)

func (s *RepositoryStore) rootTreeIDForCommit(ctx context.Context, commitID string) (string, error) {
	var rootTreeID string
	err := s.db.QueryRowContext(ctx, `
		select root_tree_id
		from commits
		where id = $1
	`, commitID).Scan(&rootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rootTreeID, err
}

func rootTreeIDForCommitTx(ctx context.Context, tx *sql.Tx, commitID string) (string, error) {
	var rootTreeID string
	err := tx.QueryRowContext(ctx, `
		select root_tree_id
		from commits
		where id = $1
	`, commitID).Scan(&rootTreeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rootTreeID, err
}

func (s *RepositoryStore) getFileAtCommitTx(ctx context.Context, tx *sql.Tx, commitID, p string) (*FileEntry, error) {
	rootTreeID, err := rootTreeIDForCommitTx(ctx, tx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getFileFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) getEntryAtCommitTx(ctx context.Context, tx *sql.Tx, commitID, p string) (*TreeEntry, error) {
	rootTreeID, err := rootTreeIDForCommitTx(ctx, tx, commitID)
	if err != nil {
		return nil, err
	}
	return s.getEntryFromTree(ctx, rootTreeID, p)
}

func (s *RepositoryStore) getFileFromTree(ctx context.Context, rootTreeID, p string) (*FileEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entry, err := s.trees.GetFile(ctx, rootTreeID, p)
	if errors.Is(err, treestore.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	file := fileEntryFromTree(*entry)
	return &file, nil
}

func (s *RepositoryStore) getEntryFromTree(ctx context.Context, rootTreeID, p string) (*TreeEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entry, err := s.trees.GetEntry(ctx, rootTreeID, p)
	if errors.Is(err, treestore.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := treeEntryFromTree(*entry)
	return &out, nil
}

func (s *RepositoryStore) listDirectoryFromTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	entries, err := s.trees.ListDirectory(ctx, rootTreeID, p)
	if err != nil {
		return nil, err
	}
	out := make([]TreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, treeEntryFromTree(entry))
	}
	return out, nil
}

func (s *RepositoryStore) listFilesFromTree(ctx context.Context, rootTreeID, prefix string) ([]FileEntry, error) {
	if s.trees == nil {
		return nil, fmt.Errorf("tree store is not configured")
	}
	files, err := s.trees.ListFiles(ctx, rootTreeID, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(files))
	for _, file := range files {
		out = append(out, fileEntryFromTree(file))
	}
	return out, nil
}
