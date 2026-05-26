package postgres

import "github.com/gitslice-io/gitslice/internal/storage"

var (
	_ storage.AuthStore       = (*AuthStore)(nil)
	_ storage.BlobStore       = (*BlobStore)(nil)
	_ storage.ChangesetStore  = (*ChangesetStore)(nil)
	_ storage.RepositoryStore = (*RepositoryStore)(nil)
	_ storage.SliceStore      = (*SliceStore)(nil)
)
