package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
)

// agentKeyTargetTx finds the target subject for API key management:
//   - agentAccount == "": the actor itself (user or agent), locking its subject row;
//   - agentAccount != "": the agent whose personal account is agentAccount, when
//     actorID is the agent itself, its controller_subject_id, or an owner of its
//     account. Anyone else gets ErrNotFound so the call does not reveal which
//     accounts are agents.
func (s *AuthStore) agentKeyTargetTx(ctx context.Context, tx *sql.Tx, actorID, agentAccount string) (subjectID, account string, err error) {
	actorID = strings.TrimSpace(actorID)
	agentAccount = strings.TrimSpace(agentAccount)
	if agentAccount == "" {
		if actorID == "" {
			return "", "", ErrUnauthenticated
		}
		err = tx.QueryRowContext(ctx, `
			select s.id, coalesce((
				select a.slug
				from account_memberships m
				join accounts a on a.id = m.account_id
				where m.subject_id = s.id
				  and a.kind = 'personal'
				  and `+notOthersAgentAccountSQL+`
				order by a.slug
				limit 1
			), '')
			from subjects s
			where s.id = $1
			for update of s
		`, actorID).Scan(&subjectID, &account)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("%w: subject not found", ErrNotFound)
		}
		return subjectID, account, err
	}
	if agentAccount, err = normalizeSlug(agentAccount, "agent"); err != nil {
		return "", "", err
	}
	err = tx.QueryRowContext(ctx, `
		select s.id, a.slug
		from subjects s
		join account_memberships self_m on self_m.subject_id = s.id
		join accounts a on a.id = self_m.account_id
		where a.kind = 'personal'
		  and a.slug = $1
		  and s.kind = 'agent'
		  and not exists (
			select 1 from agent_registrations r2
			where r2.account_id = a.id and r2.subject_id <> s.id
		  )
		  and (
			s.id = $2
			or s.controller_subject_id = $2
			or exists (
				select 1 from account_memberships m
				where m.account_id = a.id and m.subject_id = $2 and m.role = 'owner'
			)
		  )
		limit 1
		for update of s
	`, agentAccount, actorID).Scan(&subjectID, &account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: no agent account you own or are signed in as", ErrNotFound)
	}
	return subjectID, account, err
}

const apiKeyColumns = `
	id, subject_id, name, created_at, last_used_at, expires_at, revoked_at,
	slice_ids is not null, coalesce(to_json(slice_ids)::text, '[]'),
	coalesce(max_role, ''), coalesce(parent_key_id, ''),
	purpose, daemon_id, conversation_id`

func scanAPIKey(row scanner) (storage.APIKey, error) {
	var key storage.APIKey
	var lastUsed, expires, revoked sql.NullTime
	var sliceIDsJSON string
	if err := row.Scan(
		&key.ID, &key.SubjectID, &key.Name, &key.CreatedAt, &lastUsed, &expires, &revoked,
		&key.HasSliceScope, &sliceIDsJSON, &key.MaxRole, &key.ParentKeyID,
		&key.Purpose, &key.DaemonID, &key.ConversationID,
	); err != nil {
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
	if key.HasSliceScope {
		if err := json.Unmarshal([]byte(sliceIDsJSON), &key.SliceIDs); err != nil {
			return storage.APIKey{}, err
		}
		if key.SliceIDs == nil {
			key.SliceIDs = []string{}
		}
	}
	return key, nil
}

func (s *AuthStore) ListAPIKeys(ctx context.Context, actorID, agentAccount string) (string, string, []storage.APIKey, error) {
	type listResult struct {
		subjectID string
		account   string
		keys      []storage.APIKey
	}
	res, err := inTx(ctx, s.db, func(tx *sql.Tx) (listResult, error) {
		subjectID, account, err := s.agentKeyTargetTx(ctx, tx, actorID, agentAccount)
		if err != nil {
			return listResult{}, err
		}
		principal, hasPrincipal := authctx.PrincipalFrom(ctx)
		if hasPrincipal && principal.KeyID != "" && subjectID != principal.SubjectID {
			return listResult{}, fmt.Errorf("%w: an API key cannot list keys for another subject", ErrUnauthorized)
		}
		keys, err := querySlice(ctx, tx, func(rows *sql.Rows) (storage.APIKey, error) {
			return scanAPIKey(rows)
		}, `select `+apiKeyColumns+` from api_keys where subject_id = $1 order by created_at desc, id`, subjectID)
		if err != nil {
			return listResult{}, err
		}
		if hasPrincipal && principal.KeyID != "" {
			keys = storage.FilterAPIKeysForCallerKey(keys, principal.KeyID)
		}
		return listResult{subjectID: subjectID, account: account, keys: keys}, nil
	})
	if err != nil {
		return "", "", nil, err
	}
	return res.subjectID, res.account, res.keys, nil
}

func (s *AuthStore) CreateAPIKey(ctx context.Context, in storage.CreateAPIKeyInput) (storage.APIKey, string, string, string, error) {
	now := time.Now().UTC()
	if in.ParentKeyID == "" {
		if principal, ok := authctx.PrincipalFrom(ctx); ok && principal.KeyID != "" {
			in.ParentKeyID = principal.KeyID
		}
	}
	normalized, err := storage.NormalizeCreateAPIKeyInput(in, now)
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	secret, keyID, hashed, err := newAPIKey()
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	type createResult struct {
		key       storage.APIKey
		subjectID string
		account   string
	}
	res, err := inTx(ctx, s.db, func(tx *sql.Tx) (createResult, error) {
		subjectID, account, err := s.agentKeyTargetTx(ctx, tx, normalized.ActorID, normalized.AgentAccount)
		if err != nil {
			return createResult{}, err
		}
		if principal, ok := authctx.PrincipalFrom(ctx); ok && principal.KeyID != "" && subjectID != principal.SubjectID {
			return createResult{}, fmt.Errorf("%w: an API key cannot create keys for another subject", ErrUnauthorized)
		}
		if normalized.ParentKeyID != "" {
			parent, err := s.loadActiveParentAPIKeyTx(ctx, tx, normalized.ParentKeyID)
			if err != nil {
				return createResult{}, err
			}
			if parent.SubjectID != subjectID {
				return createResult{}, fmt.Errorf("%w: an API key cannot create keys for another subject", ErrUnauthorized)
			}
			normalized, err = storage.AttenuateAPIKeyInput(parent, normalized, now)
			if err != nil {
				return createResult{}, err
			}
		}
		var active int
		if err := tx.QueryRowContext(ctx, `
			select count(*) from api_keys
			where subject_id = $1 and revoked_at is null and (expires_at is null or expires_at > now())
		`, subjectID).Scan(&active); err != nil {
			return createResult{}, err
		}
		if active >= storage.MaxActiveAPIKeys {
			label := account
			if label == "" {
				label = subjectID
			}
			return createResult{}, fmt.Errorf("%w: %s already has %d active API keys; revoke one first", ErrConflict, label, active)
		}
		var expiresAt sql.NullTime
		if !normalized.ExpiresAt.IsZero() {
			expiresAt = sql.NullTime{Time: normalized.ExpiresAt, Valid: true}
		}
		var sliceIDsArg []string
		if normalized.HasSliceScope {
			sliceIDsArg = slices.Clone(normalized.SliceIDs)
			if sliceIDsArg == nil {
				sliceIDsArg = []string{}
			}
		}
		key, err := scanAPIKey(tx.QueryRowContext(ctx, `
			insert into api_keys(
				id, subject_id, name, token_hash, created_at, expires_at,
				slice_ids, max_role, parent_key_id, purpose, daemon_id, conversation_id
			)
			values (
				$1, $2, $3, $4, now(), $5,
				case when $6::boolean then coalesce($7::text[], '{}'::text[]) else null end,
				nullif($8, ''), nullif($9, ''), $10, $11, $12
			)
			returning `+apiKeyColumns,
			keyID, subjectID, normalized.Name, hashed, expiresAt,
			normalized.HasSliceScope, sliceIDsArg, normalized.MaxRole, normalized.ParentKeyID,
			normalized.Purpose, normalized.DaemonID, normalized.ConversationID,
		))
		if err != nil {
			return createResult{}, err
		}
		return createResult{key: key, subjectID: subjectID, account: account}, nil
	})
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	return res.key, secret, res.subjectID, res.account, nil
}

// loadActiveParentAPIKeyTx locks the parent key row and verifies every ancestor
// in its parent_key_id chain is unrevoked and unexpired.
func (s *AuthStore) loadActiveParentAPIKeyTx(ctx context.Context, tx *sql.Tx, parentKeyID string) (storage.APIKey, error) {
	// Lock the parent row first so concurrent revocation serializes cleanly.
	var dummy string
	err := tx.QueryRowContext(ctx, `select id from api_keys where id = $1 for update`, parentKeyID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.APIKey{}, fmt.Errorf("%w: parent API key not found", ErrUnauthenticated)
	}
	if err != nil {
		return storage.APIKey{}, err
	}
	key, err := scanAPIKey(tx.QueryRowContext(ctx, `
		with recursive chain as (
			select k.*, 0 as depth, array[k.id] as visited
			from api_keys k
			where k.id = $1
			  and k.revoked_at is null
			  and (k.expires_at is null or k.expires_at > now())
			union all
			select p.*, c.depth + 1, c.visited || p.id
			from api_keys p
			join chain c on p.id = c.parent_key_id
			where p.revoked_at is null
			  and (p.expires_at is null or p.expires_at > now())
			  and not (p.id = any(c.visited))
		)
		select id, subject_id, name, created_at, last_used_at, expires_at, revoked_at,
		       slice_ids is not null, coalesce(to_json(slice_ids)::text, '[]'),
		       coalesce(max_role, ''), coalesce(parent_key_id, ''),
		       purpose, daemon_id, conversation_id
		from chain
		where depth = 0
		  and exists (select 1 from chain root where root.parent_key_id is null)
	`, parentKeyID))
	if errors.Is(err, sql.ErrNoRows) {
		return storage.APIKey{}, fmt.Errorf("%w: parent API key is revoked or expired", ErrUnauthenticated)
	}
	return key, err
}

func (s *AuthStore) RevokeAPIKey(ctx context.Context, actorID, agentAccount, keyID string) (storage.APIKey, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return storage.APIKey{}, fmt.Errorf("%w: key id is required", ErrInvalid)
	}
	return inTx(ctx, s.db, func(tx *sql.Tx) (storage.APIKey, error) {
		subjectID, _, err := s.agentKeyTargetTx(ctx, tx, actorID, agentAccount)
		if err != nil {
			return storage.APIKey{}, err
		}
		if principal, ok := authctx.PrincipalFrom(ctx); ok && principal.KeyID != "" {
			if subjectID != principal.SubjectID {
				return storage.APIKey{}, fmt.Errorf("%w: an API key cannot revoke keys for another subject", ErrUnauthorized)
			}
			if err := s.authorizeKeyRevokeByCallerKeyTx(ctx, tx, subjectID, principal.KeyID, keyID); err != nil {
				return storage.APIKey{}, err
			}
		}
		key, err := scanAPIKey(tx.QueryRowContext(ctx, `
			with recursive descendants as (
				select id, array[id] as visited
				from api_keys
				where id = $1 and subject_id = $2
				union all
				select k.id, d.visited || k.id
				from api_keys k
				join descendants d on k.parent_key_id = d.id
				where not (k.id = any(d.visited))
			),
			updated as (
				update api_keys
				set revoked_at = coalesce(revoked_at, now())
				from descendants
				where api_keys.id = descendants.id
				returning api_keys.id, api_keys.subject_id, api_keys.name, api_keys.created_at,
				          api_keys.last_used_at, api_keys.expires_at, api_keys.revoked_at,
				          api_keys.slice_ids, api_keys.max_role, api_keys.parent_key_id,
				          api_keys.purpose, api_keys.daemon_id, api_keys.conversation_id
			)
			select id, subject_id, name, created_at, last_used_at, expires_at, revoked_at,
			       slice_ids is not null, coalesce(to_json(slice_ids)::text, '[]'),
			       coalesce(max_role, ''), coalesce(parent_key_id, ''),
			       purpose, daemon_id, conversation_id
			from updated
			where id = $1
		`, keyID, subjectID))
		if errors.Is(err, sql.ErrNoRows) {
			return storage.APIKey{}, fmt.Errorf("%w: no such API key for this agent", ErrNotFound)
		}
		if err != nil {
			return storage.APIKey{}, err
		}
		return key, nil
	})
}

func (s *AuthStore) authorizeKeyRevokeByCallerKeyTx(ctx context.Context, tx *sql.Tx, subjectID, callerKeyID, targetKeyID string) error {
	var hasScope bool
	var maxRole, parentID string
	err := tx.QueryRowContext(ctx, `
		select slice_ids is not null, coalesce(max_role, ''), coalesce(parent_key_id, '')
		from api_keys
		where id = $1 and subject_id = $2
	`, callerKeyID, subjectID).Scan(&hasScope, &maxRole, &parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	// Root uncapped/unscoped keys may revoke any key belonging to the same subject.
	if !hasScope && maxRole == "" && parentID == "" {
		return nil
	}
	var targetExists bool
	if err := tx.QueryRowContext(ctx, `
		select exists(select 1 from api_keys where id = $1 and subject_id = $2)
	`, targetKeyID, subjectID).Scan(&targetExists); err != nil {
		return err
	}
	if !targetExists {
		return fmt.Errorf("%w: no such API key for this agent", ErrNotFound)
	}
	if targetKeyID == callerKeyID {
		return nil
	}
	var isDescendant bool
	if err := tx.QueryRowContext(ctx, `
		with recursive descendants as (
			select id, array[id] as visited
			from api_keys
			where id = $1 and subject_id = $2
			union all
			select k.id, d.visited || k.id
			from api_keys k
			join descendants d on k.parent_key_id = d.id
			where not (k.id = any(d.visited))
		)
		select exists(select 1 from descendants where id = $3)
	`, callerKeyID, subjectID, targetKeyID).Scan(&isDescendant); err != nil {
		return err
	}
	if !isDescendant {
		return fmt.Errorf("%w: an attenuated or child API key can only revoke itself or its descendants", ErrUnauthorized)
	}
	return nil
}
