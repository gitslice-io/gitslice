package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

func (s *AuthStore) CreateAgentInvite(ctx context.Context, in storage.CreateAgentInviteInput) (*storage.CreateAgentInviteResult, error) {
	actorID := strings.TrimSpace(in.ActorID)
	if actorID == "" {
		return nil, ErrUnauthenticated
	}
	suggested := strings.TrimSpace(in.SuggestedUsername)
	if suggested != "" {
		normalized, err := normalizeSignupUsername(suggested)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		suggested = normalized
	}
	sliceID := strings.TrimSpace(in.SliceID)
	sliceRole, err := storage.NormalizeDelegatedAgentSliceRole(sliceID, in.SliceRole)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expiresAt := in.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(storage.DefaultAgentInviteTTL)
	}
	if !expiresAt.After(now) {
		return nil, fmt.Errorf("%w: invite expiry must be in the future", ErrInvalid)
	}
	if expiresAt.Sub(now) > storage.MaxAgentInviteTTL+time.Minute {
		return nil, fmt.Errorf("%w: invite expiry cannot exceed 7 days", ErrInvalid)
	}

	inviteID, err := objectid.RandomID("inv")
	if err != nil {
		return nil, err
	}
	token, err := objectid.RandomID(strings.TrimSuffix(storage.AgentInviteTokenPrefix, "_"))
	if err != nil {
		return nil, err
	}
	hashed := tokenHash(token)

	return inTx(ctx, s.db, func(tx *sql.Tx) (*storage.CreateAgentInviteResult, error) {
		var actorKind string
		err := tx.QueryRowContext(ctx, `select kind from subjects where id = $1`, actorID).Scan(&actorKind)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUnauthenticated
		}
		if err != nil {
			return nil, err
		}
		if actorKind != "user" {
			return nil, fmt.Errorf("%w: only human users can create agent invites", ErrUnauthorized)
		}
		var activeCount int
		if err := tx.QueryRowContext(ctx, `
			select count(*) from agent_invites
			where created_by_subject_id = $1
			  and consumed_at is null
			  and revoked_at is null
			  and expires_at > now()
		`, actorID).Scan(&activeCount); err != nil {
			return nil, err
		}
		if activeCount >= storage.MaxActiveAgentInvites {
			return nil, fmt.Errorf("%w: too many active agent invites (limit %d)", storage.ErrResourceExhausted, storage.MaxActiveAgentInvites)
		}
		var createdAt time.Time
		if err := tx.QueryRowContext(ctx, `
			insert into agent_invites(
				id, token_hash, created_by_subject_id,
				slice_id, slice_account, slice_slug, slice_role,
				suggested_username, expires_at, created_at
			)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
			returning created_at
		`, inviteID, hashed, actorID, sliceID,
			strings.TrimSpace(in.SliceAccount),
			strings.TrimSpace(in.SliceSlug),
			sliceRole, suggested, expiresAt,
		).Scan(&createdAt); err != nil {
			return nil, err
		}
		return &storage.CreateAgentInviteResult{
			Invite: storage.AgentInvite{
				ID:                inviteID,
				CreatedBy:         actorID,
				SliceID:           sliceID,
				SliceAccount:      strings.TrimSpace(in.SliceAccount),
				SliceSlug:         strings.TrimSpace(in.SliceSlug),
				SliceRole:         sliceRole,
				SuggestedUsername: suggested,
				ExpiresAt:         expiresAt,
				CreatedAt:         createdAt.UTC(),
			},
			Token: token,
		}, nil
	})
}

func (s *AuthStore) ListAgentInvites(ctx context.Context, actorID string) ([]storage.AgentInvite, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, ErrUnauthenticated
	}
	rows, err := s.db.QueryContext(ctx, `
		select i.id, i.created_by_subject_id, i.slice_id, i.slice_account, i.slice_slug,
		       i.slice_role, i.suggested_username, i.expires_at, i.created_at,
		       i.consumed_at, i.revoked_at, coalesce(a.slug, '')
		from agent_invites i
		left join agent_registrations r on r.subject_id = i.consumed_by_subject_id
		left join accounts a on a.id = r.account_id
		where i.created_by_subject_id = $1
		order by i.created_at desc, i.id desc
		limit 100
	`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.AgentInvite
	for rows.Next() {
		var inv storage.AgentInvite
		var consumedAt, revokedAt sql.NullTime
		if err := rows.Scan(
			&inv.ID, &inv.CreatedBy, &inv.SliceID, &inv.SliceAccount, &inv.SliceSlug,
			&inv.SliceRole, &inv.SuggestedUsername, &inv.ExpiresAt, &inv.CreatedAt,
			&consumedAt, &revokedAt, &inv.ConsumedByAccount,
		); err != nil {
			return nil, err
		}
		inv.ExpiresAt = inv.ExpiresAt.UTC()
		inv.CreatedAt = inv.CreatedAt.UTC()
		if consumedAt.Valid {
			inv.ConsumedAt = consumedAt.Time.UTC()
		}
		if revokedAt.Valid {
			inv.RevokedAt = revokedAt.Time.UTC()
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *AuthStore) RevokeAgentInvite(ctx context.Context, actorID, inviteID string) error {
	actorID = strings.TrimSpace(actorID)
	inviteID = strings.TrimSpace(inviteID)
	if actorID == "" {
		return ErrUnauthenticated
	}
	if inviteID == "" {
		return fmt.Errorf("%w: invite_id is required", ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx, `
		update agent_invites
		set revoked_at = now()
		where id = $1 and created_by_subject_id = $2 and consumed_at is null and revoked_at is null
	`, inviteID, actorID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: active agent invite not found", ErrNotFound)
	}
	return nil
}

func (s *AuthStore) JoinAgentInvite(ctx context.Context, in storage.JoinAgentInviteInput) (*storage.JoinAgentInviteResult, error) {
	rawToken := strings.TrimSpace(in.Token)
	if !strings.HasPrefix(rawToken, storage.AgentInviteTokenPrefix) || len(rawToken) < len(storage.AgentInviteTokenPrefix)+16 {
		return nil, fmt.Errorf("%w: invalid or expired agent invite token", ErrUnauthenticated)
	}
	hashed := tokenHash(rawToken)
	agentSubjectID, err := objectid.RandomID("agent")
	if err != nil {
		return nil, err
	}
	apiKey, keyID, hashedKey, err := newAPIKey()
	if err != nil {
		return nil, err
	}

	return inTx(ctx, s.db, func(tx *sql.Tx) (*storage.JoinAgentInviteResult, error) {
		var (
			inviteID          string
			createdBy         string
			sliceID           string
			sliceAccount      string
			sliceSlug         string
			sliceRole         string
			suggestedUsername string
			expiresAt         time.Time
			consumedAt        sql.NullTime
			revokedAt         sql.NullTime
		)
		err := tx.QueryRowContext(ctx, `
			select id, created_by_subject_id, slice_id, slice_account, slice_slug,
			       slice_role, suggested_username, expires_at, consumed_at, revoked_at
			from agent_invites
			where token_hash = $1
			for update
		`, hashed).Scan(
			&inviteID, &createdBy, &sliceID, &sliceAccount, &sliceSlug,
			&sliceRole, &suggestedUsername, &expiresAt, &consumedAt, &revokedAt,
		)
		if errors.Is(err, sql.ErrNoRows) || consumedAt.Valid || revokedAt.Valid || !expiresAt.After(time.Now().UTC()) {
			return nil, fmt.Errorf("%w: invalid or expired agent invite token", ErrUnauthenticated)
		}
		if err != nil {
			return nil, err
		}

		desiredUsername := strings.TrimSpace(in.Username)
		if desiredUsername == "" {
			desiredUsername = strings.TrimSpace(suggestedUsername)
		}
		if desiredUsername == "" {
			suffix, err := objectid.RandomID("agent")
			if err != nil {
				return nil, err
			}
			desiredUsername = "agent-" + strings.TrimPrefix(suffix, "agent_")[:8]
		}
		username, err := normalizeSignupUsername(desiredUsername)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		displayName, err := storage.NormalizeDelegatedAgentDisplayName(in.DisplayName, username)
		if err != nil {
			return nil, err
		}

		if _, err := tx.ExecContext(ctx, `
			insert into subjects(id, kind, display_name, controller_subject_id, created_at)
			values ($1, 'agent', $2, $3, now())
		`, agentSubjectID, displayName, createdBy); err != nil {
			return nil, err
		}
		accountID := signupAccountID(username)
		res, err := tx.ExecContext(ctx, `
			insert into accounts(id, slug, kind, created_at, updated_at)
			values ($1, $2, 'personal', now(), now())
			on conflict (slug) do nothing
		`, accountID, username)
		if err != nil {
			return nil, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 0 {
			return nil, fmt.Errorf("%w: username %q is not available", ErrConflict, username)
		}
		for _, memberID := range []string{agentSubjectID, createdBy} {
			if _, err := tx.ExecContext(ctx, `
				insert into account_memberships(account_id, subject_id, role, created_at)
				values ($1, $2, 'owner', now())
				on conflict do nothing
			`, accountID, memberID); err != nil {
				return nil, err
			}
		}
		if err := s.provisionHomeTx(ctx, tx, accountID, username, agentSubjectID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into agent_registrations(subject_id, account_id, owner_email, claimed_at, claimed_by_subject_id, created_at)
			values ($1, $2, '', now(), $3, now())
		`, agentSubjectID, accountID, createdBy); err != nil {
			return nil, err
		}
		if sliceID != "" {
			if _, err := tx.ExecContext(ctx, `
				insert into slice_members(slice_id, subject_id, role, added_by, created_at)
				values ($1, $2, $3, $4, now())
				on conflict (slice_id, subject_id)
				do update set role = excluded.role, added_by = excluded.added_by
			`, sliceID, agentSubjectID, sliceRole, createdBy); err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			insert into api_keys(id, subject_id, name, token_hash, created_at)
			values ($1, $2, 'default', $3, now())
		`, keyID, agentSubjectID, hashedKey); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			update agent_invites
			set consumed_at = now(), consumed_by_subject_id = $2
			where id = $1
		`, inviteID, agentSubjectID); err != nil {
			return nil, err
		}
		return &storage.JoinAgentInviteResult{
			AgentSubjectID: agentSubjectID,
			Account:        username,
			APIKey:         apiKey,
			SliceAccount:   sliceAccount,
			SliceSlug:      sliceSlug,
			SliceRole:      sliceRole,
		}, nil
	})
}
