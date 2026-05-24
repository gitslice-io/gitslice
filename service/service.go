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
	Auth        *AuthService
	Repository  *RepositoryService
	Blob        *BlobService
	Slice       *SliceService
	Workspace   *WorkspaceService
	Changeset   *ChangesetService
}

type Stores struct {
	Auth       *postgres.AuthStore
	Blobs      *postgres.BlobStore
	Changesets *postgres.ChangesetStore
	Repository *postgres.RepositoryStore
	Slices     *postgres.SliceStore
}

func New(stores Stores, objectStore ObjectStore) *Handlers {
	validator := diffValidator{
		Repository: stores.Repository,
		Slices:     stores.Slices,
	}
	return &Handlers{
		FakeAccount: &FakeAccountService{Auth: stores.Auth},
		Auth:        &AuthService{},
		Repository: &RepositoryService{
			Auth:        stores.Auth,
			Blobs:       stores.Blobs,
			Changesets:  stores.Changesets,
			Repository:  stores.Repository,
			Slices:      stores.Slices,
			ObjectStore: objectStore,
			validator:   validator,
		},
		Blob:  &BlobService{Blobs: stores.Blobs, ObjectStore: objectStore},
		Slice: &SliceService{Auth: stores.Auth, Slices: stores.Slices},
		Workspace: &WorkspaceService{
			Auth:        stores.Auth,
			Repository:  stores.Repository,
			Slices:      stores.Slices,
			ObjectStore: objectStore,
			validator:   validator,
		},
		Changeset: &ChangesetService{
			Auth:       stores.Auth,
			Changesets: stores.Changesets,
			Slices:     stores.Slices,
			validator:  validator,
		},
	}
}
