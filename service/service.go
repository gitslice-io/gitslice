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

type Handlers struct {
	FakeAccount *FakeAccountService
	Repository  *RepositoryService
	Blob        *BlobService
	Slice       *SliceService
	Workspace   *WorkspaceService
	Changeset   *ChangesetService
}

func New(store *postgres.Store, objectStore ObjectStore) *Handlers {
	validator := diffValidator{
		Repository: store.Repository(),
		Slices:     store.Slices(),
	}
	return &Handlers{
		FakeAccount: &FakeAccountService{Auth: store.Auth()},
		Repository:  &RepositoryService{Repository: store.Repository(), ObjectStore: objectStore},
		Blob:        &BlobService{Blobs: store.Blobs(), ObjectStore: objectStore},
		Slice:       &SliceService{Auth: store.Auth(), Slices: store.Slices()},
		Workspace: &WorkspaceService{
			Auth:        store.Auth(),
			Repository:  store.Repository(),
			Slices:      store.Slices(),
			ObjectStore: objectStore,
			validator:   validator,
		},
		Changeset: &ChangesetService{
			Auth:       store.Auth(),
			Changesets: store.Changesets(),
			Slices:     store.Slices(),
			validator:  validator,
		},
	}
}
