package service

import (
	"context"
	"io"

	"github.com/gitslice-io/gitslice/internal/storage"
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
	Auth       storage.AuthStore
	Blobs      storage.BlobStore
	Changesets storage.ChangesetStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
}

func New(stores Stores, objectStore ObjectStore) *Handlers {
	validator := diffValidator{
		Blobs:      stores.Blobs,
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
		Blob: &BlobService{Auth: stores.Auth, Blobs: stores.Blobs, Slices: stores.Slices, ObjectStore: objectStore},
		Slice: &SliceService{
			Auth:       stores.Auth,
			Repository: stores.Repository,
			Slices:     stores.Slices,
		},
		Workspace: &WorkspaceService{
			Auth:        stores.Auth,
			Repository:  stores.Repository,
			Slices:      stores.Slices,
			ObjectStore: objectStore,
			validator:   validator,
		},
		Changeset: &ChangesetService{
			Auth:        stores.Auth,
			Blobs:       stores.Blobs,
			Changesets:  stores.Changesets,
			Repository:  stores.Repository,
			Slices:      stores.Slices,
			ObjectStore: objectStore,
			validator:   validator,
		},
	}
}
