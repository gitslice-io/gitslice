package postgres

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func createTestSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create schema ` + schema); err != nil {
		t.Fatal(err)
	}
}

func dropTestSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`drop schema if exists ` + schema + ` cascade`); err != nil {
		t.Fatal(err)
	}
}

func databaseURLWithSearchPath(t *testing.T, rawURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func sanitizeSchemaName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "test"
	}
	return fmt.Sprintf("%.48s", b.String())
}
