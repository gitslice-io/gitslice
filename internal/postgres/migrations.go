package postgres

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
)

const migrationAdvisoryLockKey int32 = 724942

func (d *DB) Migrate(ctx context.Context) error {
	// Each migration runs in its own transaction, and takes a transaction-level
	// advisory lock first, so two migrate runs never apply the same file twice.
	// A transaction-level lock (not a session lock) is released by its commit or
	// rollback, which also holds through a transaction-mode connection pooler
	// such as Neon's, where a session lock can stay behind on a pooled backend.
	if _, err := d.db.ExecContext(ctx, `
		create table if not exists schema_migrations(
			filename text primary key,
			applied_at timestamptz not null default now()
		)
	`); err != nil {
		return err
	}
	for _, m := range loadMigrations() {
		if err := d.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.filename, err)
		}
	}

	if d.repository.trees != nil {
		if err := d.repository.trees.EnsureEmptyRoot(ctx); err != nil {
			return err
		}
	}
	return d.seedBootstrap(ctx)
}

func (d *DB) applyMigration(ctx context.Context, m sqlMigration) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `select pg_advisory_xact_lock(hashtext(current_schema()), $1)`, migrationAdvisoryLockKey); err != nil {
		return err
	}
	var applied bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from schema_migrations where filename = $1)`, m.filename).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return nil
	}
	for _, stmt := range m.statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into schema_migrations(filename) values ($1)`, m.filename); err != nil {
		return err
	}
	return tx.Commit()
}

//go:embed migrations/*.sql
var migrationFS embed.FS

type sqlMigration struct {
	filename   string
	statements []string
}

func loadMigrations() []sqlMigration {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		panic(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	out := make([]sqlMigration, 0, len(names))
	for _, name := range names {
		raw, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			panic(err)
		}
		var statements []string
		for _, stmt := range strings.Split(string(raw), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt != "" {
				statements = append(statements, stmt)
			}
		}
		out = append(out, sqlMigration{filename: name, statements: statements})
	}
	return out
}
