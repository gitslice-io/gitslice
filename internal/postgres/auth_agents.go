package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// RegisterAgent creates an agent subject, its personal account (admin
// membership + home slice), the claimable owner-email registration, and the
// agent's first API key, all in one transaction.
func (s *AuthStore) RegisterAgent(ctx context.Context, in storage.RegisterAgentInput) (*storage.RegisteredAgent, error) {
	username, err := normalizeSignupUsername(in.Username)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	ownerEmail, err := storage.NormalizeOwnerEmail(in.OwnerEmail)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = username
	}
	subjectID, err := objectid.RandomID("agent")
	if err != nil {
		return nil, err
	}
	token, keyID, hashedToken, err := newAPIKey()
	if err != nil {
		return nil, err
	}

	registrationTokenHash, err := registrationTokenHashFor(in.RegistrationToken)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if registrationTokenHash.Valid {
		resumed, err := s.resumeAgentRegistrationTx(ctx, tx, registrationTokenHash.String, username, ownerEmail, keyID, hashedToken)
		if err != nil {
			return nil, err
		}
		if resumed != nil {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			resumed.APIKey = token
			return resumed, nil
		}
	}

	if _, err := tx.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, created_at)
		values ($1, 'agent', $2, now())
	`, subjectID, displayName); err != nil {
		return nil, err
	}
	accountID, err := s.provisionAccountForSubject(ctx, tx, subjectID, username, displayName)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into agent_registrations(subject_id, account_id, owner_email, registration_token_hash, created_at)
		values ($1, $2, $3, $4, now())
	`, subjectID, accountID, ownerEmail, registrationTokenHash); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into api_keys(id, subject_id, name, token_hash, created_at)
		values ($1, $2, 'default', $3, now())
	`, keyID, subjectID, hashedToken); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &storage.RegisteredAgent{SubjectID: subjectID, Account: username, APIKey: token}, nil
}

// registrationTokenHashFor validates an optional client registration token and
// returns its hash, or an invalid NullString when none was given.
func registrationTokenHashFor(token string) (sql.NullString, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return sql.NullString{}, nil
	}
	if len(token) < storage.MinRegistrationTokenLength {
		return sql.NullString{}, fmt.Errorf("%w: registration token must be at least %d characters", ErrInvalid, storage.MinRegistrationTokenLength)
	}
	return sql.NullString{String: tokenHash(token), Valid: true}, nil
}

// resumeAgentRegistrationTx finishes a retried RegisterAgent. If a
// registration exists for the token it must match the username and owner
// email; the agent's never-used keys (e.g. one whose response was lost) are
// revoked and a fresh key is issued. It returns nil when there is nothing to
// resume.
func (s *AuthStore) resumeAgentRegistrationTx(ctx context.Context, tx *sql.Tx, tokenHashValue, username, ownerEmail, keyID, hashedKey string) (*storage.RegisteredAgent, error) {
	var subjectID, account, existingEmail string
	err := tx.QueryRowContext(ctx, `
		select r.subject_id, a.slug, r.owner_email
		from agent_registrations r
		join accounts a on a.id = r.account_id
		where r.registration_token_hash = $1
		for update of r
	`, tokenHashValue).Scan(&subjectID, &account, &existingEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if account != username || existingEmail != ownerEmail {
		return nil, fmt.Errorf("%w: registration token was already used for a different agent", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `
		update api_keys
		set revoked_at = now()
		where subject_id = $1 and last_used_at is null and revoked_at is null
	`, subjectID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into api_keys(id, subject_id, name, token_hash, created_at)
		values ($1, $2, 'default', $3, now())
	`, keyID, subjectID, hashedKey); err != nil {
		return nil, err
	}
	return &storage.RegisteredAgent{SubjectID: subjectID, Account: account, Resumed: true}, nil
}

func newAPIKey() (token, keyID, hashedToken string, err error) {
	token, err = objectid.RandomID(strings.TrimSuffix(storage.APIKeyPrefix, "_"))
	if err != nil {
		return "", "", "", err
	}
	keyID, err = objectid.RandomID("key")
	if err != nil {
		return "", "", "", err
	}
	return token, keyID, tokenHash(token), nil
}

func (s *AuthStore) ListOwnedAgents(ctx context.Context, subjectID string) ([]storage.OwnedAgent, error) {
	rows, err := s.db.QueryContext(ctx, `
		select r.subject_id, agent.display_name, a.slug, r.created_at,
		       r.claimed_at,
		       (select max(k.last_used_at) from api_keys k where k.subject_id = r.subject_id)
		from agent_registrations r
		join subjects agent on agent.id = r.subject_id
		join accounts a on a.id = r.account_id
		join account_memberships m on m.account_id = r.account_id
		where m.subject_id = $1 and m.role = 'owner' and r.subject_id <> $1
		order by r.created_at, r.subject_id
	`, strings.TrimSpace(subjectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.OwnedAgent
	for rows.Next() {
		var agent storage.OwnedAgent
		var claimedAt, lastActiveAt sql.NullTime
		if err := rows.Scan(&agent.AgentSubjectID, &agent.AgentDisplayName, &agent.Account, &agent.RegisteredAt, &claimedAt, &lastActiveAt); err != nil {
			return nil, err
		}
		agent.RegisteredAt = agent.RegisteredAt.UTC()
		if claimedAt.Valid {
			agent.ClaimedAt = claimedAt.Time.UTC()
		}
		if lastActiveAt.Valid {
			agent.LastActiveAt = lastActiveAt.Time.UTC()
		}
		out = append(out, agent)
	}
	return out, rows.Err()
}

// notOthersAgentAccountSQL is a predicate over `a` (accounts) and `m`
// (account_memberships) that is false when the account belongs to an agent
// other than m.subject_id. A human who claims an agent becomes an owner of the
// agent's personal account; that account must never count as the human's own
// personal account (username, home, ChooseUsername).
const notOthersAgentAccountSQL = `not exists (
	select 1 from agent_registrations r
	where r.account_id = a.id and r.subject_id <> m.subject_id
)`

func (s *AuthStore) ListPendingClaims(ctx context.Context, subjectID string) ([]storage.PendingClaim, error) {
	rows, err := s.db.QueryContext(ctx, `
		select r.subject_id, agent.display_name, a.slug, r.owner_email, r.created_at
		from agent_registrations r
		join subjects agent on agent.id = r.subject_id
		join accounts a on a.id = r.account_id
		where r.claimed_at is null
		  and r.subject_id <> $1
		  and exists (
			select 1 from subject_emails e
			where e.subject_id = $1 and e.email = r.owner_email
		  )
		order by r.created_at, r.subject_id
	`, strings.TrimSpace(subjectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.PendingClaim
	for rows.Next() {
		var claim storage.PendingClaim
		if err := rows.Scan(&claim.AgentSubjectID, &claim.AgentDisplayName, &claim.Account, &claim.OwnerEmail, &claim.CreatedAt); err != nil {
			return nil, err
		}
		claim.CreatedAt = claim.CreatedAt.UTC()
		out = append(out, claim)
	}
	return out, rows.Err()
}

func (s *AuthStore) AcceptClaim(ctx context.Context, subjectID, agentSubjectID string) (string, error) {
	subjectID = strings.TrimSpace(subjectID)
	agentSubjectID = strings.TrimSpace(agentSubjectID)
	if subjectID == "" || agentSubjectID == "" || subjectID == agentSubjectID {
		return "", ErrNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var accountID, account string
	err = tx.QueryRowContext(ctx, `
		select r.account_id, a.slug
		from agent_registrations r
		join accounts a on a.id = r.account_id
		where r.subject_id = $2
		  and r.claimed_at is null
		  and exists (
			select 1 from subject_emails e
			where e.subject_id = $1 and e.email = r.owner_email
		  )
		for update of r
	`, subjectID, agentSubjectID).Scan(&accountID, &account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no pending claim for this agent", ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values ($1, $2, 'owner', now())
		on conflict do nothing
	`, accountID, subjectID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		update agent_registrations
		set claimed_at = now(), claimed_by_subject_id = $2
		where subject_id = $1
	`, agentSubjectID, subjectID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return account, nil
}
