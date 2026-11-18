package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
)

func (s *SliceStore) ListSliceSecrets(ctx context.Context, sliceID string) ([]storage.SliceSecret, error) {
	account, prefixes, err := s.secretSliceTx(ctx, s.db, sliceID)
	if err != nil {
		return nil, err
	}
	secrets, err := s.listSecrets(ctx, `where accounts.slug = $1`, account)
	if err != nil {
		return nil, err
	}
	var out []storage.SliceSecret
	for _, secret := range secrets {
		if storage.SecretTouches(secret.Paths, prefixes) {
			out = append(out, storage.SliceSecret{Name: secret.Name, SecretID: secret.ID, Paths: secret.Paths})
		}
	}
	return out, nil
}

func (s *SliceStore) SetSliceSecret(ctx context.Context, sliceID, name, value, actorID string) error {
	if !storage.ValidSliceSecretName(name) {
		return fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
	}
	if len(value) > storage.MaxSecretValueBytes {
		return fmt.Errorf("%w: a secret value can be at most %d KiB", ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	err := execInTx(ctx, s.db, func(tx *sql.Tx) error {
		accountID, err := lockSecretAccountTx(ctx, tx, `
			select accounts.id from accounts join slices on slices.account_id = accounts.id
			where slices.id = $1 and slices.deleted_at is null for update of accounts
		`, strings.TrimSpace(sliceID))
		if err != nil {
			return err
		}
		account, prefixes, err := s.secretSliceTx(ctx, tx, sliceID)
		if err != nil {
			return err
		}
		named, err := namedSecretsOnPathsTx(ctx, tx, name, prefixes)
		if err != nil {
			return err
		}
		edit, err := storage.PlanSetSliceSecret(name, prefixes, named)
		if err != nil {
			return err
		}
		if edit.Create {
			if err := requireSecretRoomTx(ctx, tx, accountID); err != nil {
				return err
			}
			id, err := objectid.RandomID("sec")
			if err != nil {
				return err
			}
			stored, err := s.sealSecretValue(account, name, prefixes, value)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				insert into secrets(id, account_id, name, value, created_by, updated_by, created_at, updated_at)
				values ($1, $2, $3, $4, $5, $5, now(), now())
			`, id, accountID, name, stored, actorID); err != nil {
				return secretWriteError(err)
			}
			return insertSecretPathsTx(ctx, tx, id, name, prefixes)
		}
		if len(edit.AddPaths) > 0 {
			if err := insertSecretPathsTx(ctx, tx, edit.Update[0], name, edit.AddPaths); err != nil {
				return err
			}
		}
		for _, id := range edit.Update {
			currentPaths, err := secretPathsForIDTx(ctx, tx, id)
			if err != nil {
				return err
			}
			stored, err := s.sealSecretValue(account, name, currentPaths, value)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				update secrets set value = $2, updated_by = $3, updated_at = now() where id = $1
			`, id, stored, actorID); err != nil {
				return err
			}
		}
		return nil
	})
	return secretWriteError(err)
}

func (s *SliceStore) DeleteSliceSecret(ctx context.Context, sliceID, name, actorID string) error {
	if !storage.ValidSliceSecretName(name) {
		return fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
	}
	return execInTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := lockSecretAccountTx(ctx, tx, `
			select accounts.id from accounts join slices on slices.account_id = accounts.id
			where slices.id = $1 and slices.deleted_at is null for update of accounts
		`, strings.TrimSpace(sliceID)); err != nil {
			return err
		}
		account, prefixes, err := s.secretSliceTx(ctx, tx, sliceID)
		if err != nil {
			return err
		}
		named, err := namedSecretsOnPathsTx(ctx, tx, name, prefixes)
		if err != nil {
			return err
		}
		if len(named) == 0 {
			var ancestors []string
			for _, prefix := range prefixes {
				ancestors = append(ancestors, storage.SecretFolderAncestors(prefix)...)
			}
			var otherID, otherPath string
			err := tx.QueryRowContext(ctx, `
				select secret_id, path from secret_paths where name = $1 and path = any($2) order by length(path) desc limit 1
			`, name, ancestors).Scan(&otherID, &otherPath)
			if err == nil {
				return fmt.Errorf("%w: %s comes from secret %s on %s, a folder above this slice; delete it or change its folders with gs secret", ErrConflict, name, otherID, otherPath)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return nil
		}
		for _, secret := range named {
			if storage.PathsWithin(secret.Paths, prefixes) {
				if _, err := tx.ExecContext(ctx, `delete from secrets where id = $1`, secret.ID); err != nil {
					return err
				}
				continue
			}
			var currentValue string
			if err := tx.QueryRowContext(ctx, `select value from secrets where id = $1`, secret.ID).Scan(&currentValue); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `delete from secret_paths where secret_id = $1 and path = any($2)`, secret.ID, prefixes); err != nil {
				return err
			}
			remainingPaths, err := secretPathsForIDTx(ctx, tx, secret.ID)
			if err != nil {
				return err
			}
			if s.Secrets != nil && secretbox.IsSealed(currentValue) {
				plain, err := s.openSecretValue(account, name, secret.Paths, currentValue)
				if err != nil {
					return err
				}
				resealed, err := s.sealSecretValue(account, name, remainingPaths, plain)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `update secrets set value = $2, updated_by = $3, updated_at = now() where id = $1`, secret.ID, resealed, actorID); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `update secrets set updated_by = $2, updated_at = now() where id = $1`, secret.ID, actorID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// secretSliceTx returns a live slice's account and its included paths.
func (s *SliceStore) secretSliceTx(ctx context.Context, q dbtx, sliceID string) (string, []string, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return "", nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	var account string
	err := q.QueryRowContext(ctx, `
		select accounts.slug from slices join accounts on accounts.id = slices.account_id
		where slices.id = $1 and slices.deleted_at is null
	`, sliceID).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	prefixes, err := querySlice(ctx, q, scanString, `select prefix from slice_included_paths where slice_id = $1 order by prefix`, sliceID)
	if err != nil {
		return "", nil, err
	}
	if len(prefixes) == 0 {
		return "", nil, fmt.Errorf("%w: slice %s includes no paths", ErrInvalid, sliceID)
	}
	return account, prefixes, nil
}

// namedSecretsOnPathsTx returns the secrets named name with one of the
// paths, each with all its paths.
func namedSecretsOnPathsTx(ctx context.Context, q dbtx, name string, onPaths []string) ([]storage.Secret, error) {
	rows, err := q.QueryContext(ctx, `
		select secret_paths.secret_id, secret_paths.path
		from secret_paths
		where secret_paths.name = $1
		  and secret_paths.secret_id in (select secret_id from secret_paths where name = $1 and path = any($2))
		order by secret_paths.secret_id, secret_paths.path
	`, name, onPaths)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Secret
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != id {
			out = append(out, storage.Secret{ID: id, Name: name})
		}
		out[len(out)-1].Paths = append(out[len(out)-1].Paths, path)
	}
	return out, rows.Err()
}

func secretPathsForIDTx(ctx context.Context, q dbtx, secretID string) ([]string, error) {
	return querySlice(ctx, q, scanString, `select path from secret_paths where secret_id = $1 order by path`, secretID)
}

func secretAAD(account, name string, secretPaths []string) []byte {
	sorted := append([]string(nil), secretPaths...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("gitslice.secret.v2\x00")
	b.WriteString(strings.TrimSpace(account))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(name))
	for _, p := range sorted {
		b.WriteByte(0)
		b.WriteString(p)
	}
	return []byte(b.String())
}

func (s *SliceStore) sealSecretValue(account, name string, secretPaths []string, value string) (string, error) {
	if len(value) > storage.MaxSecretValueBytes {
		return "", fmt.Errorf("%w: a secret value can be at most %d KiB", ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	// Configured servers write enc:v2 AAD-bound envelope-encrypted values. Reads
	// keep backward-compatible paths for enc:v1 and unprefixed legacy rows.
	if s.Secrets == nil {
		return value, nil
	}
	sealed, err := s.Secrets.SealWithAAD(value, secretAAD(account, name, secretPaths))
	if err != nil {
		return "", fmt.Errorf("seal secret: %w", err)
	}
	return sealed, nil
}

func (s *SliceStore) openSecretValue(account, name string, secretPaths []string, value string) (string, error) {
	if s.Secrets == nil {
		if secretbox.IsSealed(value) {
			return "", fmt.Errorf("secret %q is encrypted but no secrets key is configured", name)
		}
		return value, nil
	}
	opened, err := s.Secrets.OpenWithAAD(value, secretAAD(account, name, secretPaths))
	if err != nil {
		return "", fmt.Errorf("open secret %q: %w", name, err)
	}
	return opened, nil
}

// lockSecretAccountTx runs query, which selects one account id with FOR
// UPDATE, and returns the id; ErrNotFound when it selects none.
func lockSecretAccountTx(ctx context.Context, tx *sql.Tx, query string, arg string) (string, error) {
	var accountID string
	err := tx.QueryRowContext(ctx, query, arg).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return accountID, err
}

func requireSecretRoomTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `select count(*) from secrets where account_id = $1`, accountID).Scan(&count); err != nil {
		return err
	}
	if count >= storage.MaxSecretsPerAccount {
		return fmt.Errorf("%w: an account can have at most %d secrets", ErrConflict, storage.MaxSecretsPerAccount)
	}
	return nil
}

// secretPathConflictTx refuses paths that already have another secret named
// name.
func secretPathConflictTx(ctx context.Context, tx *sql.Tx, secretID, name string, secretPaths []string) error {
	var other, path string
	err := tx.QueryRowContext(ctx, `
		select secret_id, path from secret_paths
		where path = any($1) and name = $2 and secret_id <> $3
		order by path
		limit 1
	`, secretPaths, name, secretID).Scan(&other, &path)
	if err == nil {
		return fmt.Errorf("%w: %s already has secret %s named %s", ErrConflict, path, other, name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// replaceSecretPathsTx gives a secret exactly these paths. The old scope
// columns are cleared, so a rolled-back server does not see a stale scope.
func replaceSecretPathsTx(ctx context.Context, tx *sql.Tx, secretID, name string, secretPaths []string) error {
	if err := secretPathConflictTx(ctx, tx, secretID, name, secretPaths); err != nil {
		return err
	}
	for _, stmt := range []string{
		`delete from secret_paths where secret_id = $1`,
		`delete from secret_slices where secret_id = $1`,
		`update secrets set all_slices = false where id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, secretID); err != nil {
			return err
		}
	}
	return insertSecretPathsTx(ctx, tx, secretID, name, secretPaths)
}

func insertSecretPathsTx(ctx context.Context, tx *sql.Tx, secretID, name string, secretPaths []string) error {
	sorted := append([]string(nil), secretPaths...)
	sort.Strings(sorted)
	for _, path := range sorted {
		if _, err := tx.ExecContext(ctx, `
			insert into secret_paths(secret_id, path, name) values ($1, $2, $3)
		`, secretID, path, name); err != nil {
			return secretWriteError(err)
		}
	}
	return nil
}

// secretWriteError reports a unique-index violation, which a concurrent
// write the checks did not see would cause, as a conflict.
func secretWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: another secret of that name already applies to one of these folders", ErrConflict)
	}
	return err
}
