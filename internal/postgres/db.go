package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"gitslice.io/gitslice/internal/treestore"
)

type DB struct {
	db            *sql.DB
	auth          *AuthStore
	blobs         *BlobStore
	repository    *RepositoryStore
	slices        *SliceStore
	changesets    *ChangesetStore
	agents        *AgentStore
	checks        *CheckStore
	events        *EventStore
	automations   *AutomationStore
	feedback      *FeedbackStore
	blames        *BlameStore
	mirrors       *GitMirrorStore
	packedObjects *PackedObjectStore
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
	db.SetConnMaxIdleTime(2 * time.Minute)
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

func (d *DB) Agents() *AgentStore {
	return d.agents
}

func (d *DB) Checks() *CheckStore {
	return d.checks
}

// Triggers returns the trigger run store (design/34_triggers.md).
func (d *DB) Automations() *AutomationStore {
	return d.automations
}

// Events returns the event log store used by triggers and Git mirrors.
func (d *DB) Events() *EventStore {
	return d.events
}

// Blames returns the blame snapshot store (design/27_blame.md).
func (d *DB) Blames() *BlameStore {
	return d.blames
}

// GitMirrors returns the slice Git mirror store (design/35_git_mirrors.md).
func (d *DB) GitMirrors() *GitMirrorStore {
	return d.mirrors
}

// Feedback returns the feedback store (design/25_feedback.md).
func (d *DB) Feedback() *FeedbackStore {
	return d.feedback
}

// PackedObjects returns the Postgres byte-range index for packed object storage.
func (d *DB) PackedObjects() *PackedObjectStore {
	return d.packedObjects
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
	d.agents = &AgentStore{db: d.db}
	d.checks = &CheckStore{db: d.db}
	d.events = &EventStore{db: d.db}
	d.automations = &AutomationStore{db: d.db}
	d.feedback = &FeedbackStore{db: d.db}
	d.blames = &BlameStore{db: d.db}
	d.mirrors = &GitMirrorStore{db: d.db}
	d.packedObjects = &PackedObjectStore{db: d.db}
}
