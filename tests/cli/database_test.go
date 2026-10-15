package cli_test

import (
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// uniqueSchema builds a Postgres-safe, unique schema name. Postgres truncates
// identifiers to 63 bytes, so the unique token must survive truncation: we trim
// the test-name hint up front to keep the whole name within the limit, leaving
// the token (and its uniqueness) fully intact. The old time-of-day suffix had
// only second resolution and was silently truncated away for long test names,
// causing reruns to collide on a stale schema.
// schemaSeq disambiguates schema names requested within a single process,
// even in the same nanosecond.
var schemaSeq atomic.Uint64

func uniqueSchema(prefix string, t *testing.T) string {
	const maxLen = 63 // Postgres NAMEDATALEN - 1
	// The token must be unique across concurrent `go test` processes that share
	// one database, not just within this process. The nanosecond clock alone is
	// not enough: a coarse monotonic clock can hand the same UnixNano to two
	// processes, which then derive the same schema name and clobber each other
	// (createSchema drops-then-creates, so the second process wipes the first's
	// migrated schema and they collapse onto one). The pid distinguishes
	// concurrent processes; the sequence distinguishes calls within one.
	token := strings.Join([]string{
		strconv.FormatInt(time.Now().UnixNano(), 36),
		strconv.FormatInt(int64(os.Getpid()), 36),
		strconv.FormatUint(schemaSeq.Add(1), 36),
	}, "_")
	hint := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	budget := maxLen - len(prefix) - len(token) - 1 // room for the "_" separator
	if budget < 0 {
		name := prefix + token
		if len(name) > maxLen {
			name = name[:maxLen]
		}
		return name
	}
	if len(hint) > budget {
		hint = hint[:budget]
	}
	return prefix + hint + "_" + token
}

func createSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Drop any leftover schema from an interrupted run so create is idempotent.
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create schema ` + pqIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
}

func dropSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Logf("open db for cleanup: %v", err)
		return
	}
	defer db.Close()
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Logf("drop schema %s: %v", schema, err)
	}
}

func pqIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func databaseURLWithSearchPath(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string for CLI e2e tests")
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}
