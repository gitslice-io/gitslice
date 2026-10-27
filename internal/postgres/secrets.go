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

// Secrets (design/33_secrets.md). Writes lock the account's row, so the checks
// that keep a slice from having two secrets of one name, and the per-account
// limit, see every other write; the unique indexes back them up.

const secretColumns = `secrets.id, accounts.slug, secrets.name, secrets.all_slices, secrets.created_by, secrets.updated_by, secrets.created_at, secrets.updated_at`

func scanSecret(row rowScanner) (*storage.Secret, error) {
	var secret storage.Secret
	if err := row.Scan(&secret.ID, &secret.Account, &secret.Name, &secret.AllSlices, &secret.CreatedBy, &secret.UpdatedBy, &secret.CreatedAt, &secret.UpdatedAt); err != nil {
		return nil, err
	}
	return &secret, nil
}

func (s *SliceStore) CreateSecret(ctx context.Context, account, name, value string, scope storage.SecretScope, actorID string) (*storage.Secret, error) {
	account = strings.TrimSpace(account)
	if !storage.ValidSliceSecretName(name) {
		return nil, fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
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
	sliceIDs, err := secretScopeSlicesTx(ctx, tx, accountID, account, scope)
	if err != nil {
		return nil, err
	}
	if err := secretConflictTx(ctx, tx, accountID, id, name, scope.AllSlices, sliceIDs); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into secrets(id, account_id, name, value, all_slices, created_by, updated_by, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $6, now(), now())
	`, id, accountID, name, stored, scope.AllSlices, actorID); err != nil {
		return nil, secretWriteError(err)
	}
	if err := insertSecretSlicesTx(ctx, tx, id, name, sliceIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, secretWriteError(err)
	}
	return s.GetSecret(ctx, id)
}

func (s *SliceStore) UpdateSecret(ctx context.Context, secretID string, value *string, scope *storage.SecretScope, actorID string) (*storage.Secret, error) {
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
	accountID, err := lockSecretAccountTx(ctx, tx, `
		select accounts.id from accounts join secrets on secrets.account_id = accounts.id
		where secrets.id = $1 for update of accounts
	`, secretID)
	if err != nil {
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
	if scope != nil {
		sliceIDs, err := secretScopeSlicesTx(ctx, tx, accountID, account, *scope)
		if err != nil {
			return nil, err
		}
		if err := secretConflictTx(ctx, tx, accountID, secretID, name, scope.AllSlices, sliceIDs); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `update secrets set all_slices = $2 where id = $1`, secretID, scope.AllSlices); err != nil {
			return nil, secretWriteError(err)
		}
		if _, err := tx.ExecContext(ctx, `delete from secret_slices where secret_id = $1`, secretID); err != nil {
			return nil, err
		}
		if err := insertSecretSlicesTx(ctx, tx, secretID, name, sliceIDs); err != nil {
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
	scoped, err := s.secretSlices(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Slices = scoped[out[i].ID]
	}
	return out, nil
}

// secretSlices returns the slices each secret lists, by slice name.
func (s *SliceStore) secretSlices(ctx context.Context, secretIDs []string) (map[string][]storage.SecretSlice, error) {
	if len(secretIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select secret_slices.secret_id, slices.id, slices.slug
		from secret_slices join slices on slices.id = secret_slices.slice_id
		where secret_slices.secret_id = any($1) and slices.deleted_at is null
		order by slices.slug
	`, secretIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]storage.SecretSlice{}
	for rows.Next() {
		var secretID string
		var slice storage.SecretSlice
		if err := rows.Scan(&secretID, &slice.ID, &slice.Name); err != nil {
			return nil, err
		}
		out[secretID] = append(out[secretID], slice)
	}
	return out, rows.Err()
}

// sliceSecretRows are the secrets that reach a slice: those that list it, and
// the account's all-slices secrets whose names those do not cover.
const sliceSecretRows = `
	select secrets.id, secrets.name, secrets.all_slices, secrets.value
	from secrets join secret_slices on secret_slices.secret_id = secrets.id
	where secret_slices.slice_id = $1
	union all
	select secrets.id, secrets.name, secrets.all_slices, secrets.value
	from secrets join slices on slices.account_id = secrets.account_id
	where slices.id = $1 and secrets.all_slices
	  and not exists (select 1 from secret_slices where secret_slices.slice_id = $1 and secret_slices.name = secrets.name)
	order by 2`

type sliceSecretRow struct {
	id, name, value string
	allSlices       bool
}

func (s *SliceStore) sliceSecretRows(ctx context.Context, sliceID string) ([]sliceSecretRow, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return nil, fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if err := s.requireSliceExists(ctx, sliceID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, sliceSecretRows, sliceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sliceSecretRow
	for rows.Next() {
		var row sliceSecretRow
		if err := rows.Scan(&row.id, &row.name, &row.allSlices, &row.value); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *SliceStore) ListSliceSecrets(ctx context.Context, sliceID string) ([]storage.SliceSecret, error) {
	rows, err := s.sliceSecretRows(ctx, sliceID)
	if err != nil {
		return nil, err
	}
	var listed []string
	for _, row := range rows {
		if !row.allSlices {
			listed = append(listed, row.id)
		}
	}
	scoped, err := s.secretSlices(ctx, listed)
	if err != nil {
		return nil, err
	}
	out := make([]storage.SliceSecret, 0, len(rows))
	for _, row := range rows {
		secret := storage.SliceSecret{Name: row.name, SecretID: row.id, AllSlices: row.allSlices}
		for _, slice := range scoped[row.id] {
			if slice.ID != sliceID {
				secret.SharedWith = append(secret.SharedWith, slice)
			}
		}
		out = append(out, secret)
	}
	return out, nil
}

func (s *SliceStore) ListSliceSecretNames(ctx context.Context, sliceID string) ([]string, error) {
	rows, err := s.sliceSecretRows(ctx, sliceID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.name)
	}
	return names, nil
}

func (s *SliceStore) GetSliceSecrets(ctx context.Context, sliceID string) (map[string]string, error) {
	rows, err := s.sliceSecretRows(ctx, sliceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		value := row.value
		if s.Secrets == nil {
			if secretbox.IsSealed(value) {
				return nil, fmt.Errorf("secret %q is encrypted but no secrets key is configured", row.name)
			}
		} else {
			var err error
			if value, err = s.Secrets.Open(value); err != nil {
				return nil, fmt.Errorf("open secret %q: %w", row.name, err)
			}
		}
		out[row.name] = value
	}
	return out, nil
}

func (s *SliceStore) SetSliceSecret(ctx context.Context, sliceID, name, value, actorID string) error {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
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
	`, sliceID)
	if err != nil {
		return err
	}
	secretID, shared, err := listingSecretTx(ctx, tx, sliceID, name)
	if err != nil {
		return err
	}
	switch {
	case secretID != "" && shared > 0:
		return fmt.Errorf("%w: %s on this slice comes from secret %s, shared with %d other slice(s); change it there, or take this slice out of its scope first", ErrConflict, name, secretID, shared)
	case secretID != "":
		if _, err := tx.ExecContext(ctx, `
			update secrets set value = $2, updated_by = $3, updated_at = now() where id = $1
		`, secretID, stored, actorID); err != nil {
			return err
		}
	default:
		if err := requireSecretRoomTx(ctx, tx, accountID); err != nil {
			return err
		}
		id, err := objectid.RandomID("sec")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into secrets(id, account_id, name, value, all_slices, created_by, updated_by, created_at, updated_at)
			values ($1, $2, $3, $4, false, $5, $5, now(), now())
		`, id, accountID, name, stored, actorID); err != nil {
			return secretWriteError(err)
		}
		if err := insertSecretSlicesTx(ctx, tx, id, name, []string{sliceID}); err != nil {
			return err
		}
	}
	return secretWriteError(tx.Commit())
}

func (s *SliceStore) DeleteSliceSecret(ctx context.Context, sliceID, name, actorID string) error {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return fmt.Errorf("%w: slice_id is required", ErrInvalid)
	}
	if !storage.ValidSliceSecretName(name) {
		return fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	accountID, err := lockSecretAccountTx(ctx, tx, `
		select accounts.id from accounts join slices on slices.account_id = accounts.id
		where slices.id = $1 and slices.deleted_at is null for update of accounts
	`, sliceID)
	if err != nil {
		return err
	}
	secretID, shared, err := listingSecretTx(ctx, tx, sliceID, name)
	if err != nil {
		return err
	}
	switch {
	case secretID != "" && shared > 0:
		if _, err := tx.ExecContext(ctx, `delete from secret_slices where secret_id = $1 and slice_id = $2`, secretID, sliceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update secrets set updated_by = $2, updated_at = now() where id = $1`, secretID, actorID); err != nil {
			return err
		}
	case secretID != "":
		if _, err := tx.ExecContext(ctx, `delete from secrets where id = $1`, secretID); err != nil {
			return err
		}
	default:
		var allSlicesID string
		err := tx.QueryRowContext(ctx, `
			select id from secrets where account_id = $1 and name = $2 and all_slices
		`, accountID, name).Scan(&allSlicesID)
		if err == nil {
			return fmt.Errorf("%w: %s comes from secret %s, which applies to every slice of the account; delete it or change its scope with gs secret", ErrConflict, name, allSlicesID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return tx.Commit()
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

// secretScopeSlicesTx checks a scope against the account and returns the ids
// of the slices it lists, sorted and without repeats (none for all slices).
func secretScopeSlicesTx(ctx context.Context, tx *sql.Tx, accountID, account string, scope storage.SecretScope) ([]string, error) {
	if scope.AllSlices {
		return nil, nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range scope.SliceIDs {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: a secret applies to all slices or to at least one slice", ErrInvalid)
	}
	sort.Strings(ids)
	rows, err := tx.QueryContext(ctx, `
		select id from slices where id = any($1) and account_id = $2 and deleted_at is null
	`, ids, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !found[id] {
			return nil, fmt.Errorf("%w: slice %s is not a slice of account %s", ErrInvalid, id, account)
		}
	}
	return ids, nil
}

// secretConflictTx refuses a scope that would give a slice a second secret
// named name, or the account a second all-slices one.
func secretConflictTx(ctx context.Context, tx *sql.Tx, accountID, secretID, name string, allSlices bool, sliceIDs []string) error {
	if allSlices {
		var other string
		err := tx.QueryRowContext(ctx, `
			select id from secrets where account_id = $1 and name = $2 and all_slices and id <> $3
		`, accountID, name, secretID).Scan(&other)
		if err == nil {
			return fmt.Errorf("%w: secret %s already gives %s to every slice of the account", ErrConflict, other, name)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var other, slice string
	err := tx.QueryRowContext(ctx, `
		select secret_slices.secret_id, slices.slug
		from secret_slices join slices on slices.id = secret_slices.slice_id
		where secret_slices.slice_id = any($1) and secret_slices.name = $2 and secret_slices.secret_id <> $3
		order by slices.slug
		limit 1
	`, sliceIDs, name, secretID).Scan(&other, &slice)
	if err == nil {
		return fmt.Errorf("%w: slice %s already has secret %s named %s", ErrConflict, slice, other, name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func insertSecretSlicesTx(ctx context.Context, tx *sql.Tx, secretID, name string, sliceIDs []string) error {
	for _, sliceID := range sliceIDs {
		if _, err := tx.ExecContext(ctx, `
			insert into secret_slices(secret_id, slice_id, name) values ($1, $2, $3)
		`, secretID, sliceID, name); err != nil {
			return secretWriteError(err)
		}
	}
	return nil
}

// listingSecretTx finds the secret named name that lists the slice, and how
// many other slices it lists.
func listingSecretTx(ctx context.Context, tx *sql.Tx, sliceID, name string) (string, int, error) {
	var secretID string
	var others int
	err := tx.QueryRowContext(ctx, `
		select secret_slices.secret_id,
		       (select count(*) from secret_slices o where o.secret_id = secret_slices.secret_id and o.slice_id <> $1)
		from secret_slices
		where secret_slices.slice_id = $1 and secret_slices.name = $2
	`, sliceID, name).Scan(&secretID, &others)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	return secretID, others, err
}

// secretWriteError reports a unique-index violation, which a concurrent
// write the checks did not see would cause, as a conflict.
func secretWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: another secret of that name already reaches one of these slices", ErrConflict)
	}
	return err
}
