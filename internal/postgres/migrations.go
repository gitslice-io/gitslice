package postgres

import (
	"context"
	"embed"
	"strings"
)

func (d *DB) Migrate(ctx context.Context) error {
	for _, stmt := range migrationStatements() {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
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

func migrationStatements() []string {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		panic(err)
	}
	var statements []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			panic(err)
		}
		for _, stmt := range strings.Split(string(raw), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt != "" {
				statements = append(statements, stmt)
			}
		}
	}
	return statements
}
