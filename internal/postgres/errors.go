package postgres

import "errors"

var (
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid")
	ErrNotFound        = errors.New("not found")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrUnauthorized    = errors.New("unauthorized")
)
