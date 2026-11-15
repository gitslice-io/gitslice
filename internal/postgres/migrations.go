package postgres

import (
	"context"
	"embed"
	"sort"
	"strings"
	"time"
)

const migrationAdvisoryLockKey int32 = 724942

func (d *DB) Migrate(ctx context.Context) error {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `select pg_advisory_lock(hashtext(current_schema()), $1)`, migrationAdvisoryLockKey); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, `select pg_advisory_unlock(hashtext(current_schema()), $1)`, migrationAdvisoryLockKey)
	}()

	if _, err := conn.ExecContext(ctx, `
		create table if not exists schema_migrations(
			filename text primary key,
			applied_at timestamptz not null default now()
		)
	`); err != nil {
		return err
	}

	rows, err := conn.QueryContext(ctx, `select filename from schema_migrations`)
	if err != nil {
		return err
	}
	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, m := range loadMigrations() {
		if applied[m.filename] {
			continue
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range m.statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			insert into schema_migrations(filename)
			values ($1)
			on conflict (filename) do nothing
		`, m.filename); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	if d.repository.trees != nil {
		if err := d.repository.trees.EnsureEmptyRoot(ctx); err != nil {
			return err
		}
	}
	return d.seedBootstrap(ctx)
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
