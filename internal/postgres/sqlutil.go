package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
)

// dbtx abstracts query and statement execution across *sql.DB and *sql.Tx so
// read and write helpers do not force callers to open unnecessary transactions.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanner abstracts *sql.Row and *sql.Rows for shared row-decoding helpers.
type scanner interface {
	Scan(dest ...any) error
}

// inTx executes fn inside a database transaction, rolling back on error or
// panic and committing when fn succeeds.
func inTx[T any](ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	out, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return out, nil
}

// execInTx executes a void transactional callback via inTx.
func execInTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	_, err := inTx(ctx, db, func(tx *sql.Tx) (struct{}, error) {
		return struct{}{}, fn(tx)
	})
	return err
}

// querySlice executes query against q and scans every row via scan, closing
// rows and checking rows.Err() automatically.
func querySlice[T any](ctx context.Context, q dbtx, scan func(*sql.Rows) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanString(rows *sql.Rows) (string, error) {
	var s string
	err := rows.Scan(&s)
	return s, err
}

// jsonColumn adapts a typed Go value for JSON/JSONB SQL scanning and parameter
// binding via sql.Scanner and driver.Valuer.
type jsonColumn[T any] struct {
	v *T
}

func jsonCol[T any](dst *T) jsonColumn[T] {
	return jsonColumn[T]{v: dst}
}

func jsonVal[T any](val T) jsonColumn[T] {
	return jsonColumn[T]{v: &val}
}

func (c jsonColumn[T]) Scan(src any) error {
	if c.v == nil {
		return nil
	}
	var raw []byte
	switch val := src.(type) {
	case nil:
		raw = []byte("null")
	case []byte:
		raw = val
	case string:
		raw = []byte(val)
	default:
		return fmt.Errorf("unsupported json column source %T", src)
	}
	return decodeJSON(raw, c.v)
}

func (c jsonColumn[T]) Value() (driver.Value, error) {
	if c.v == nil {
		var zero T
		return encodeJSON(zero)
	}
	return encodeJSON(*c.v)
}
