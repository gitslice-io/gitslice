package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
)

// Secrets (design/33_secrets.md). A secret applies to the folders in
// secret_paths and everything below them. Writes lock the account's row, so
// the checks that keep a folder from having two secrets of one name, and the
// per-account limit, see every other write; the unique index backs them up.

const secretColumns = `secrets.id, accounts.slug, secrets.name, secrets.created_by, secrets.updated_by, secrets.created_at, secrets.updated_at`

func scanSecret(row scanner) (*storage.Secret, error) {
	var secret storage.Secret
	if err := row.Scan(&secret.ID, &secret.Account, &secret.Name, &secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt); err != nil {
		return nil, err
	}
	return &secret, nil
}

func (s *SliceStore) CreateSecret(ctx context.Context, account, name, value string, secretPaths []string, actorID string) (*storage.Secret, error) {
	account = strings.TrimSpace(account)
	if !storage.ValidSliceSecretName(name) {
		return nil, fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
	}
	normalized, err := storage.NormalizeSecretPaths(account, secretPaths)
	if err != nil {
		return nil, err
	}
	stored, err := s.sealSecretValue(account, name, normalized, value)
	if err != nil {
		return nil, err
	}
	id, err := objectid.RandomID("sec")
	if err != nil {
		return nil, err
	}
	if err := execInTx(ctx, s.db, func(tx *sql.Tx) error {
		accountID, err := lockSecretAccountTx(ctx, tx, `select id from accounts where slug = $1 for update`, account)
		if err != nil {
			return err
		}
		if err := requireSecretRoomTx(ctx, tx, accountID); err != nil {
			return err
		}
		if err := secretPathConflictTx(ctx, tx, id, name, normalized); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into secrets(id, account_id, name, value, created_by, updated_by, created_at, updated_at)
			values ($1, $2, $3, $4, $5, $5, now(), now())
		`, id, accountID, name, stored, actorID); err != nil {
			return secretWriteError(err)
		}
		return insertSecretPathsTx(ctx, tx, id, name, normalized)
	}); err != nil {
		return nil, secretWriteError(err)
	}
	return s.GetSecret(ctx, id)
}

func (s *SliceStore) UpdateSecret(ctx context.Context, secretID string, value *string, secretPaths []string, actorID string) (*storage.Secret, error) {
	secretID = strings.TrimSpace(secretID)
	if value != nil && len(*value) > storage.MaxSecretValueBytes {
		return nil, fmt.Errorf("%w: a secret value can be at most %d KiB", ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	if err := execInTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := lockSecretAccountTx(ctx, tx, `
			select accounts.id from accounts join secrets on secrets.account_id = accounts.id
			where secrets.id = $1 for update of accounts
		`, secretID); err != nil {
			return err
		}
		var account, name, currentValue string
		if err := tx.QueryRowContext(ctx, `
			select accounts.slug, secrets.name, secrets.value from secrets join accounts on accounts.id = secrets.account_id where secrets.id = $1
		`, secretID).Scan(&account, &name, &currentValue); err != nil {
			return err
		}
		oldPaths, err := secretPathsForIDTx(ctx, tx, secretID)
		if err != nil {
			return err
		}
		targetPaths := oldPaths
		if secretPaths != nil {
			normalized, err := storage.NormalizeSecretPaths(account, secretPaths)
			if err != nil {
				return err
			}
			targetPaths = normalized
		}
		if value != nil {
			stored, err := s.sealSecretValue(account, name, targetPaths, *value)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `update secrets set value = $2 where id = $1`, secretID, stored); err != nil {
				return err
			}
		} else if secretPaths != nil && s.Secrets != nil && secretbox.IsSealed(currentValue) {
			plain, err := s.openSecretValue(account, name, oldPaths, currentValue)
			if err != nil {
				return err
			}
			resealed, err := s.sealSecretValue(account, name, targetPaths, plain)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `update secrets set value = $2 where id = $1`, secretID, resealed); err != nil {
				return err
			}
		}
		if secretPaths != nil {
			if err := replaceSecretPathsTx(ctx, tx, secretID, name, targetPaths); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `update secrets set updated_by = $2, updated_at = now() where id = $1`, secretID, actorID)
		return err
	}); err != nil {
		return nil, secretWriteError(err)
	}
	return s.GetSecret(ctx, secretID)
}

func (s *SliceStore) DeleteSecret(ctx context.Context, secretID string) error {
	res, err := s.db.ExecContext(ctx, `delete from secrets where id = $1`, strings.TrimSpace(secretID))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SliceStore) GetSecret(ctx context.Context, secretID string) (*storage.Secret, error) {
	secrets, err := s.listSecrets(ctx, `where secrets.id = $1`, strings.TrimSpace(secretID))
	if err != nil {
		return nil, err
	}
	if len(secrets) == 0 {
		return nil, ErrNotFound
	}
	return &secrets[0], nil
}

func (s *SliceStore) ListSecrets(ctx context.Context, account string) ([]storage.Secret, error) {
	account = strings.TrimSpace(account)
	var exists bool
	if err := s.db.QueryRowContext(ctx, `select exists(select 1 from accounts where slug = $1)`, account).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	return s.listSecrets(ctx, `where accounts.slug = $1`, account)
}

func (s *SliceStore) listSecrets(ctx context.Context, where string, args ...any) ([]storage.Secret, error) {
	var ids []string
	out, err := querySlice(ctx, s.db, func(rows *sql.Rows) (storage.Secret, error) {
		secret, err := scanSecret(rows)
		if err != nil {
			return storage.Secret{}, err
		}
		ids = append(ids, secret.ID)
		return *secret, nil
	}, `
		select `+secretColumns+`
		from secrets join accounts on accounts.id = secrets.account_id
		`+where+`
		order by secrets.name, secrets.created_at, secrets.id
	`, args...)
	if err != nil {
		return nil, err
	}
	byID, err := s.secretPaths(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Paths = byID[out[i].ID]
	}
	return out, nil
}

// secretPaths returns each secret's folders, sorted.
func (s *SliceStore) secretPaths(ctx context.Context, secretIDs []string) (map[string][]string, error) {
	if len(secretIDs) == 0 {
		return nil, nil
	}
	out := map[string][]string{}
	_, err := querySlice(ctx, s.db, func(rows *sql.Rows) (struct{}, error) {
		var secretID, path string
		if err := rows.Scan(&secretID, &path); err != nil {
			return struct{}{}, err
		}
		out[secretID] = append(out[secretID], path)
		return struct{}{}, nil
	}, `select secret_id, path from secret_paths where secret_id = any($1) order by path`, secretIDs)
	return out, err
}

func (s *SliceStore) SecretValuesAt(ctx context.Context, folder string) (map[string]string, error) {
	cleaned, err := paths.CanonicalPrefix(strings.TrimSpace(folder))
	if err != nil {
		return nil, fmt.Errorf("%w: folder %q: %v", ErrInvalid, folder, err)
	}
	type matchedSecret struct {
		id, account, name, value string
	}
	var ids []string
	matched, err := querySlice(ctx, s.db, func(rows *sql.Rows) (matchedSecret, error) {
		var m matchedSecret
		if err := rows.Scan(&m.id, &m.account, &m.name, &m.value); err != nil {
			return matchedSecret{}, err
		}
		ids = append(ids, m.id)
		return m, nil
	}, `
		select distinct on (secret_paths.name)
		       secrets.id, accounts.slug, secret_paths.name, secrets.value
		from secret_paths
		join secrets on secrets.id = secret_paths.secret_id
		join accounts on accounts.id = secrets.account_id
		where secret_paths.path = any($1)
		order by secret_paths.name, length(secret_paths.path) desc
	`, storage.SecretFolderAncestors(cleaned))
	if err != nil {
		return nil, err
	}
	pathsByID, err := s.secretPaths(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, m := range matched {
		opened, err := s.openSecretValue(m.account, m.name, pathsByID[m.id], m.value)
		if err != nil {
			return nil, err
		}
		out[m.name] = opened
	}
	return out, nil
}
