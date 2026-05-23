package service

import (
	"context"
	"io"

	"github.com/gitslice-io/gitslice/internal/postgres"
)

type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type Services struct {
	Store       *postgres.Store
	ObjectStore ObjectStore
}

func New(store *postgres.Store, objectStore ObjectStore) *Services {
	return &Services{Store: store, ObjectStore: objectStore}
}
