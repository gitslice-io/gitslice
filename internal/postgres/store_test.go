package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/treestore"
)

func newPostgresTestStore(t *testing.T) (context.Context, *DB) {
	t.Helper()
	ctx, store, _ := newPostgresTestStoreWithObjects(t)
	return ctx, store
}

func newPostgresTestStoreWithObjects(t *testing.T) (context.Context, *DB, *filesystem.Store) {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run Postgres storage tests")
	}
	ctx := context.Background()
	schema := "gitslice_store_" + sanitizeSchemaName(t.Name()) + "_" + time.Now().Format("150405000000")
	createTestSchema(t, databaseURL, schema)
	store, err := Open(ctx, databaseURLWithSearchPath(t, databaseURL, schema))
	if err != nil {
		t.Fatal(err)
	}
	objectStore, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.SetTreeStore(treestore.New(objectStore))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	seedStoreTestFixture(t, ctx, store)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		dropTestSchema(t, databaseURL, schema)
	})
	return ctx, store, objectStore
}

// seedStoreTestFixture provisions the acme account, dev subjects, and the
// payment/backend slices the lower-level storage tests operate against. Prod no
// longer seeds dev data (it is created at runtime via ChooseUsername), so the
// fixture lives here in test code.
func seedStoreTestFixture(t *testing.T, ctx context.Context, store *DB) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at)
		values
			('user_alice', 'user', 'Alice', now()),
			('user_bob', 'user', 'Bob', now()),
			('ci_bot', 'service_account', 'CI Bot', now())
		on conflict (id) do nothing
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into accounts(id, slug, kind, created_at, updated_at)
		values ('acct_acme', 'acme', 'org', now(), now())
		on conflict (id) do nothing
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values
			('acct_acme', 'user_alice', 'admin', now()),
			('acct_acme', 'user_bob', 'writer', now()),
			('acct_acme', 'ci_bot', 'writer', now())
		on conflict do nothing
	`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedStoreTestSlice(t, ctx, tx, "slice_acme_payment", "payment", []string{"/acme/payment"})
	seedStoreTestSlice(t, ctx, tx, "slice_acme_backend", "backend", []string{"/acme/backend", "/acme/payment/shared"})
	if err := ensureAccountRootDirectoryTx(ctx, tx, "acme", "system", store.repository.trees); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedStoreTestSlice(t *testing.T, ctx context.Context, tx *sql.Tx, id, slug string, included []string) {
	t.Helper()
	includedJSON, err := encodeJSON(included)
	if err != nil {
		t.Fatal(err)
	}
	emptyChecksJSON, err := encodeJSON([]string{})
	if err != nil {
		t.Fatal(err)
	}
	hash := definitionHash(id, 1, included, "private", 0, nil)
	if _, err := tx.ExecContext(ctx, `
		insert into slices(id, account_id, slug, version, definition_hash, visibility, included_paths, created_at, updated_at)
		values ($1, 'acct_acme', $2, 1, $3, 'private', $4, now(), now())
		on conflict (id) do nothing
	`, id, slug, hash, includedJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		insert into slice_definition_versions(
			slice_id, version, definition_hash, visibility, included_paths,
			required_approvals, required_checks, created_at, created_by
		)
		values ($1, 1, $2, 'private', $3, 0, $4, now(), 'system')
		on conflict do nothing
	`, id, hash, includedJSON, emptyChecksJSON); err != nil {
		t.Fatal(err)
	}
	if err := syncSliceIncludedPathsTx(ctx, tx, id, included); err != nil {
		t.Fatal(err)
	}
}
