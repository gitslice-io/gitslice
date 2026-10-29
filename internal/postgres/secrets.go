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
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
)

// Secrets (design/33_secrets.md). A secret applies to the folders in
// secret_paths and everything below them. Writes lock the account's row, so
// the checks that keep a folder from having two secrets of one name, and the
// per-account limit, see every other write; the unique index backs them up.

const secretColumns = `secrets.id, accounts.slug, secrets.name, secrets.created_by, secrets.updated_by, secrets.created_at, secrets.updated_at`

func scanSecret(row rowScanner) (*storage.Secret, error) {
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
	stored, err := s.sealSecretValue(value)
	if err != nil {
		return nil, err
	}
	id, err := objectid.RandomID("sec")
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	accountID, err := lockSecretAccountTx(ctx, tx, `select id from accounts where slug = $1 for update`, account)
	if err != nil {
		return nil, err
	}
	if err := requireSecretRoomTx(ctx, tx, accountID); err != nil {
		return nil, err
	}
	if err := secretPathConflictTx(ctx, tx, id, name, normalized); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into secrets(id, account_id, name, value, created_by, updated_by, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $5, now(), now())
	`, id, accountID, name, stored, actorID); err != nil {
		return nil, secretWriteError(err)
	}
	if err := insertSecretPathsTx(ctx, tx, id, name, normalized); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, secretWriteError(err)
	}
	return s.GetSecret(ctx, id)
}

func (s *SliceStore) UpdateSecret(ctx context.Context, secretID string, value *string, secretPaths []string, actorID string) (*storage.Secret, error) {
	secretID = strings.TrimSpace(secretID)
	var stored string
	if value != nil {
		var err error
		if stored, err = s.sealSecretValue(*value); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := lockSecretAccountTx(ctx, tx, `
		select accounts.id from accounts join secrets on secrets.account_id = accounts.id
		where secrets.id = $1 for update of accounts
	`, secretID); err != nil {
		return nil, err
	}
	var account, name string
	if err := tx.QueryRowContext(ctx, `
		select accounts.slug, secrets.name from secrets join accounts on accounts.id = secrets.account_id where secrets.id = $1
	`, secretID).Scan(&account, &name); err != nil {
		return nil, err
	}
	if value != nil {
		if _, err := tx.ExecContext(ctx, `update secrets set value = $2 where id = $1`, secretID, stored); err != nil {
			return nil, err
		}
	}
	if secretPaths != nil {
		normalized, err := storage.NormalizeSecretPaths(account, secretPaths)
		if err != nil {
			return nil, err
		}
		if err := replaceSecretPathsTx(ctx, tx, secretID, name, normalized); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update secrets set updated_by = $2, updated_at = now() where id = $1`, secretID, actorID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `
		select `+secretColumns+`
		from secrets join accounts on accounts.id = secrets.account_id
		`+where+`
		order by secrets.name, secrets.created_at, secrets.id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Secret
	var ids []string
	for rows.Next() {
		secret, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *secret)
		ids = append(ids, secret.ID)
	}
	if err := rows.Err(); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `
		select secret_id, path from secret_paths where secret_id = any($1) order by path
	`, secretIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var secretID, path string
		if err := rows.Scan(&secretID, &path); err != nil {
			return nil, err
		}
		out[secretID] = append(out[secretID], path)
	}
	return out, rows.Err()
}

func (s *SliceStore) SecretValuesAt(ctx context.Context, folder string) (map[string]string, error) {
	cleaned, err := paths.CanonicalPrefix(strings.TrimSpace(folder))
	if err != nil {
		return nil, fmt.Errorf("%w: folder %q: %v", ErrInvalid, folder, err)
	}
	// For each name, the secret whose path is the longest prefix of the folder.
	rows, err := s.db.QueryContext(ctx, `
		select distinct on (secret_paths.name) secret_paths.name, secrets.value
		from secret_paths join secrets on secrets.id = secret_paths.secret_id
		where secret_paths.path = any($1)
		order by secret_paths.name, length(secret_paths.path) desc
	`, storage.SecretFolderAncestors(cleaned))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		if value, err = s.openSecretValue(name, value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

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
	stored, err := s.sealSecretValue(value)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	accountID, err := lockSecretAccountTx(ctx, tx, `
		select accounts.id from accounts join slices on slices.account_id = accounts.id
		where slices.id = $1 and slices.deleted_at is null for update of accounts
	`, strings.TrimSpace(sliceID))
	if err != nil {
		return err
	}
	_, prefixes, err := s.secretSliceTx(ctx, tx, sliceID)
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
		if _, err := tx.ExecContext(ctx, `
			insert into secrets(id, account_id, name, value, created_by, updated_by, created_at, updated_at)
			values ($1, $2, $3, $4, $5, $5, now(), now())
		`, id, accountID, name, stored, actorID); err != nil {
			return secretWriteError(err)
		}
		if err := insertSecretPathsTx(ctx, tx, id, name, prefixes); err != nil {
			return err
		}
		return secretWriteError(tx.Commit())
	}
	for _, id := range edit.Update {
		if _, err := tx.ExecContext(ctx, `
			update secrets set value = $2, updated_by = $3, updated_at = now() where id = $1
		`, id, stored, actorID); err != nil {
			return err
		}
	}
	if len(edit.AddPaths) > 0 {
		if err := insertSecretPathsTx(ctx, tx, edit.Update[0], name, edit.AddPaths); err != nil {
			return err
		}
	}
	return secretWriteError(tx.Commit())
}

func (s *SliceStore) DeleteSliceSecret(ctx context.Context, sliceID, name, actorID string) error {
	if !storage.ValidSliceSecretName(name) {
		return fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := lockSecretAccountTx(ctx, tx, `
		select accounts.id from accounts join slices on slices.account_id = accounts.id
		where slices.id = $1 and slices.deleted_at is null for update of accounts
	`, strings.TrimSpace(sliceID)); err != nil {
		return err
	}
	_, prefixes, err := s.secretSliceTx(ctx, tx, sliceID)
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
		if _, err := tx.ExecContext(ctx, `delete from secret_paths where secret_id = $1 and path = any($2)`, secret.ID, prefixes); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update secrets set updated_by = $2, updated_at = now() where id = $1`, secret.ID, actorID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// secretQueryer is a *sql.DB or a *sql.Tx.
type secretQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// secretSliceTx returns a live slice's account and its included paths.
func (s *SliceStore) secretSliceTx(ctx context.Context, q secretQueryer, sliceID string) (string, []string, error) {
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
	rows, err := q.QueryContext(ctx, `select prefix from slice_included_paths where slice_id = $1 order by prefix`, sliceID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return "", nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if len(prefixes) == 0 {
		return "", nil, fmt.Errorf("%w: slice %s includes no paths", ErrInvalid, sliceID)
	}
	return account, prefixes, nil
}

// namedSecretsOnPathsTx returns the secrets named name with one of the
// paths, each with all its paths.
func namedSecretsOnPathsTx(ctx context.Context, tx *sql.Tx, name string, onPaths []string) ([]storage.Secret, error) {
	rows, err := tx.QueryContext(ctx, `
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

func (s *SliceStore) sealSecretValue(value string) (string, error) {
	if len(value) > storage.MaxSecretValueBytes {
		return "", fmt.Errorf("%w: a secret value can be at most %d KiB", ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	// Configured servers write enc:v1 envelope-encrypted values. Reads keep a
	// legacy-plaintext path for rows written before a key was configured.
	if s.Secrets == nil {
		return value, nil
	}
	sealed, err := s.Secrets.Seal(value)
	if err != nil {
		return "", fmt.Errorf("seal secret: %w", err)
	}
	return sealed, nil
}

func (s *SliceStore) openSecretValue(name, value string) (string, error) {
	if s.Secrets == nil {
		if secretbox.IsSealed(value) {
			return "", fmt.Errorf("secret %q is encrypted but no secrets key is configured", name)
		}
		return value, nil
	}
	opened, err := s.Secrets.Open(value)
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
