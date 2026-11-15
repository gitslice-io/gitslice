package postgres

import (
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestMigrationsTrackedOncePreservesBlameSnapshots(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	snapshot := storage.BlameSnapshot{
		CommitID:    "sha256:keep",
		Path:        "/acme/payment/keep.go",
		ContentHash: "sha256:keepcontent",
		Owners:      []string{"sha256:keep"},
	}
	if err := store.Blames().PutBlameSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	// A second Migrate (a restart or deploy) skips applied migrations, so
	// 0042_clear_blame_snapshots.sql does not run again.
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.Blames().BlameSnapshots(ctx, []string{"sha256:keep"})
	if err != nil {
		t.Fatal(err)
	}
	if got["sha256:keep"]["/acme/payment/keep.go"] == nil {
		t.Fatalf("blame snapshot wiped on second Migrate(): %+v", got)
	}

	var applied int
	if err := store.db.QueryRowContext(ctx, `select count(*) from schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if want := len(loadMigrations()); applied != want {
		t.Fatalf("schema_migrations has %d rows, want %d", applied, want)
	}
}

func TestMigrateConcurrentRunsApplyOnce(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Migrate(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// The advisory lock is transaction-scoped: none is left held afterwards.
	var held int
	if err := store.db.QueryRowContext(ctx, `
		select count(*) from pg_locks where locktype = 'advisory' and objid = $1
	`, migrationAdvisoryLockKey).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("%d advisory locks still held after Migrate", held)
	}
}
