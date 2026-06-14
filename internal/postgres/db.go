package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/treestore"
)

type DB struct {
	db         *sql.DB
	auth       *AuthStore
	blobs      *BlobStore
	repository *RepositoryStore
	slices     *SliceStore
	changesets *ChangesetStore
}

func Open(ctx context.Context, databaseURL string) (*DB, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	postgres := &DB{db: db}
	postgres.initStores()
	return postgres, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) SetTreeStore(trees *treestore.Store) {
	d.auth.trees = trees
	d.repository.trees = trees
	d.changesets.trees = trees
}

func (d *DB) Auth() *AuthStore {
	return d.auth
}

func (d *DB) Blobs() *BlobStore {
	return d.blobs
}

func (d *DB) Repository() *RepositoryStore {
	return d.repository
}

func (d *DB) Slices() *SliceStore {
	return d.slices
}

func (d *DB) Changesets() *ChangesetStore {
	return d.changesets
}

func (d *DB) initStores() {
	d.auth = &AuthStore{db: d.db}
	d.blobs = &BlobStore{db: d.db}
	d.repository = &RepositoryStore{db: d.db}
	d.slices = &SliceStore{db: d.db}
	d.changesets = &ChangesetStore{
		db:         d.db,
		repository: d.repository,
		slices:     d.slices,
	}
}
