package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/storage"
)

// A lost agent key is replaced by an owner of the agent's account: they list
// the agent's keys, create a new one, hand it over and revoke the lost one.
// The agent can do the same for itself while it still has a working key.
// See design/20_agent_signup_and_claim.md, Phase 3.

// agentKeyTargetTx finds the agent whose personal account is agentAccount ("" is
// the actor itself) and checks that actorID may manage its keys: the agent
// itself, or an owner of its account. It locks the registration, so concurrent
// creates cannot step over the key limit. Anyone else gets ErrNotFound, so the
// call does not say which accounts are agents.
func (s *AuthStore) agentKeyTargetTx(ctx context.Context, tx *sql.Tx, actorID, agentAccount string) (subjectID, account string, err error) {
	actorID = strings.TrimSpace(actorID)
	agentAccount = strings.TrimSpace(agentAccount)
	if agentAccount != "" {
		if agentAccount, err = normalizeSlug(agentAccount, "agent"); err != nil {
			return "", "", err
		}
	}
	err = tx.QueryRowContext(ctx, `
		select r.subject_id, a.slug
		from agent_registrations r
		join accounts a on a.id = r.account_id
		where (($1 = '' and r.subject_id = $2) or ($1 <> '' and a.slug = $1))
		  and (r.subject_id = $2 or exists (
			select 1 from account_memberships m
			where m.account_id = r.account_id and m.subject_id = $2 and m.role = 'owner'
		  ))
		for update of r
	`, agentAccount, actorID).Scan(&subjectID, &account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: no agent account you own or are signed in as", ErrNotFound)
	}
	return subjectID, account, err
}

const apiKeyColumns = `id, subject_id, name, created_at, last_used_at, expires_at, revoked_at`

func scanAPIKey(row scanner) (storage.APIKey, error) {
	var key storage.APIKey
	var lastUsed, expires, revoked sql.NullTime
	if err := row.Scan(&key.ID, &key.SubjectID, &key.Name, &key.CreatedAt, &lastUsed, &expires, &revoked); err != nil {
		return storage.APIKey{}, err
	}
	key.CreatedAt = key.CreatedAt.UTC()
	if lastUsed.Valid {
		key.LastUsedAt = lastUsed.Time.UTC()
	}
	if expires.Valid {
		key.ExpiresAt = expires.Time.UTC()
	}
	if revoked.Valid {
		key.RevokedAt = revoked.Time.UTC()
	}
	return key, nil
}

func (s *AuthStore) ListAPIKeys(ctx context.Context, actorID, agentAccount string) (string, string, []storage.APIKey, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", nil, err
	}
	defer tx.Rollback()
	subjectID, account, err := s.agentKeyTargetTx(ctx, tx, actorID, agentAccount)
	if err != nil {
		return "", "", nil, err
	}
	rows, err := tx.QueryContext(ctx, `select `+apiKeyColumns+` from api_keys where subject_id = $1 order by created_at desc, id`, subjectID)
	if err != nil {
		return "", "", nil, err
	}
	defer rows.Close()
	var keys []storage.APIKey
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return "", "", nil, err
		}
		keys = append(keys, key)
	}
	return subjectID, account, keys, rows.Err()
}

func (s *AuthStore) CreateAPIKey(ctx context.Context, actorID, agentAccount, name string) (storage.APIKey, string, string, string, error) {
	name, err := normalizeAPIKeyName(name)
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	secret, keyID, hashed, err := newAPIKey()
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	defer tx.Rollback()
	subjectID, account, err := s.agentKeyTargetTx(ctx, tx, actorID, agentAccount)
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `
		select count(*) from api_keys
		where subject_id = $1 and revoked_at is null and (expires_at is null or expires_at > now())
	`, subjectID).Scan(&active); err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	if active >= storage.MaxActiveAPIKeys {
		return storage.APIKey{}, "", "", "", fmt.Errorf("%w: %s already has %d active API keys; revoke one first", ErrConflict, account, active)
	}
	key, err := scanAPIKey(tx.QueryRowContext(ctx, `
		insert into api_keys(id, subject_id, name, token_hash, created_at)
		values ($1, $2, $3, $4, now())
		returning `+apiKeyColumns, keyID, subjectID, name, hashed))
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	if err := tx.Commit(); err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	return key, secret, subjectID, account, nil
}

func (s *AuthStore) RevokeAPIKey(ctx context.Context, actorID, agentAccount, keyID string) (storage.APIKey, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return storage.APIKey{}, fmt.Errorf("%w: key id is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.APIKey{}, err
	}
	defer tx.Rollback()
	subjectID, _, err := s.agentKeyTargetTx(ctx, tx, actorID, agentAccount)
	if err != nil {
		return storage.APIKey{}, err
	}
	key, err := scanAPIKey(tx.QueryRowContext(ctx, `
		update api_keys set revoked_at = coalesce(revoked_at, now())
		where id = $1 and subject_id = $2
		returning `+apiKeyColumns, keyID, subjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return storage.APIKey{}, fmt.Errorf("%w: no such API key for this agent", ErrNotFound)
	}
	if err != nil {
		return storage.APIKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.APIKey{}, err
	}
	return key, nil
}

// normalizeAPIKeyName trims a key's label and checks it. An empty label
// becomes "key".
func normalizeAPIKeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "key", nil
	}
	if utf8.RuneCountInString(name) > storage.MaxAPIKeyNameLength {
		return "", fmt.Errorf("%w: key name must be %d characters or fewer", ErrInvalid, storage.MaxAPIKeyNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: key name must not contain control characters", ErrInvalid)
		}
	}
	return name, nil
}
