package postgres

import "github.com/gitslice-io/gitslice/internal/storage"

var (
	ErrConflict        = storage.ErrConflict
	ErrInvalid         = storage.ErrInvalid
	ErrNotFound        = storage.ErrNotFound
	ErrUnauthenticated = storage.ErrUnauthenticated
	ErrUnauthorized    = storage.ErrUnauthorized
)
