package service

import (
	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/storage"
)

// changesetCore holds the shared dependencies wired once in service.New and
// embedded by both ChangesetService and ChangesetStackService so cross-service
// operations (stack restacks, stack submits, merge-on-submit, author/daemon
// attribution, and analytics) always see the complete dependency graph.
type changesetCore struct {
	Auth        storage.AuthStore
	Blobs       storage.BlobStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	Agents      storage.AgentStore
	Checks      storage.CheckStore
	ObjectStore ObjectStore
	Analytics   analytics.Client
	validator   diffValidator
	dispatcher  *checkDispatcher
	blobService *BlobService
}

func (c *changesetCore) changesets() ChangesetService {
	return ChangesetService{changesetCore: c}
}

func (c *changesetCore) stacks() ChangesetStackService {
	return ChangesetStackService{changesetCore: c}
}

func (c *changesetCore) blobs() *BlobService {
	if c != nil && c.blobService != nil {
		return c.blobService
	}
	if c == nil {
		return &BlobService{}
	}
	return &BlobService{
		Auth:        c.Auth,
		Blobs:       c.Blobs,
		Slices:      c.Slices,
		ObjectStore: c.ObjectStore,
	}
}
