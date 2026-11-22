package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

func (s *AuthStore) CreateAgentInvite(ctx context.Context, in storage.CreateAgentInviteInput) (*storage.CreateAgentInviteResult, error) {
	actorID := strings.TrimSpace(in.ActorID)
	if actorID == "" {
		return nil, storage.ErrUnauthenticated
	}
	suggested := strings.TrimSpace(in.SuggestedUsername)
	if suggested != "" {
		normalized, err := normalizeSignupUsername(suggested)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", storage.ErrInvalid, err)
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
		return nil, fmt.Errorf("%w: invite expiry must be in the future", storage.ErrInvalid)
	}
	if expiresAt.Sub(now) > storage.MaxAgentInviteTTL+time.Minute {
		return nil, fmt.Errorf("%w: invite expiry cannot exceed 7 days", storage.ErrInvalid)
	}
	inviteID, err := objectid.RandomID("inv")
	if err != nil {
		return nil, err
	}
	token, err := objectid.RandomID(strings.TrimSuffix(storage.AgentInviteTokenPrefix, "_"))
	if err != nil {
		return nil, err
	}
	hashed := memoryTokenHash(token)

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	actor, ok := s.b.subjects[actorID]
	if !ok {
		return nil, storage.ErrUnauthenticated
	}
	if actor.Kind != "user" {
		return nil, fmt.Errorf("%w: only human users can create agent invites", storage.ErrUnauthorized)
	}
	activeCount := 0
	for _, rec := range s.b.agentInvites {
		if rec.invite.CreatedBy == actorID &&
			rec.invite.ConsumedAt.IsZero() &&
			rec.invite.RevokedAt.IsZero() &&
			rec.invite.ExpiresAt.After(now) {
			activeCount++
		}
	}
	if activeCount >= storage.MaxActiveAgentInvites {
		return nil, fmt.Errorf("%w: too many active agent invites (limit %d)", storage.ErrResourceExhausted, storage.MaxActiveAgentInvites)
	}
	inv := storage.AgentInvite{
		ID:                inviteID,
		CreatedBy:         actorID,
		SliceID:           sliceID,
		SliceAccount:      strings.TrimSpace(in.SliceAccount),
		SliceSlug:         strings.TrimSpace(in.SliceSlug),
		SliceRole:         sliceRole,
		SuggestedUsername: suggested,
		ExpiresAt:         expiresAt,
		CreatedAt:         now,
	}
	s.b.agentInvites[inviteID] = &memAgentInvite{invite: inv, tokenHash: hashed}
	s.b.agentInviteByHash[hashed] = inviteID
	return &storage.CreateAgentInviteResult{Invite: inv, Token: token}, nil
}

func (s *AuthStore) ListAgentInvites(ctx context.Context, actorID string) ([]storage.AgentInvite, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, storage.ErrUnauthenticated
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.AgentInvite
	for _, rec := range s.b.agentInvites {
		if rec.invite.CreatedBy != actorID {
			continue
		}
		inv := rec.invite
		if rec.consumedBy != "" {
			if reg, ok := s.b.agentRegistrations[rec.consumedBy]; ok {
				inv.ConsumedByAccount = reg.account
			}
		}
		out = append(out, inv)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (s *AuthStore) RevokeAgentInvite(ctx context.Context, actorID, inviteID string) error {
	actorID = strings.TrimSpace(actorID)
	inviteID = strings.TrimSpace(inviteID)
	if actorID == "" {
		return storage.ErrUnauthenticated
	}
	if inviteID == "" {
		return fmt.Errorf("%w: invite_id is required", storage.ErrInvalid)
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	rec, ok := s.b.agentInvites[inviteID]
	if !ok || rec.invite.CreatedBy != actorID || !rec.invite.ConsumedAt.IsZero() || !rec.invite.RevokedAt.IsZero() {
		return fmt.Errorf("%w: active agent invite not found", storage.ErrNotFound)
	}
	rec.invite.RevokedAt = time.Now().UTC()
	return nil
}

func (s *AuthStore) JoinAgentInvite(ctx context.Context, in storage.JoinAgentInviteInput) (*storage.JoinAgentInviteResult, error) {
	rawToken := strings.TrimSpace(in.Token)
	if !strings.HasPrefix(rawToken, storage.AgentInviteTokenPrefix) || len(rawToken) < len(storage.AgentInviteTokenPrefix)+16 {
		return nil, fmt.Errorf("%w: invalid or expired agent invite token", storage.ErrUnauthenticated)
	}
	hashed := memoryTokenHash(rawToken)
	agentSubjectID, err := objectid.RandomID("agent")
	if err != nil {
		return nil, err
	}
	secret, err := objectid.RandomID(strings.TrimSuffix(storage.APIKeyPrefix, "_"))
	if err != nil {
		return nil, err
	}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	inviteID, ok := s.b.agentInviteByHash[hashed]
	if !ok {
		return nil, fmt.Errorf("%w: invalid or expired agent invite token", storage.ErrUnauthenticated)
	}
	rec := s.b.agentInvites[inviteID]
	now := time.Now().UTC()
	if rec == nil || !rec.invite.ConsumedAt.IsZero() || !rec.invite.RevokedAt.IsZero() || !rec.invite.ExpiresAt.After(now) {
		return nil, fmt.Errorf("%w: invalid or expired agent invite token", storage.ErrUnauthenticated)
	}

	desiredUsername := strings.TrimSpace(in.Username)
	if desiredUsername == "" {
		desiredUsername = strings.TrimSpace(rec.invite.SuggestedUsername)
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
		return nil, fmt.Errorf("%w: %v", storage.ErrInvalid, err)
	}
	displayName, err := storage.NormalizeDelegatedAgentDisplayName(in.DisplayName, username)
	if err != nil {
		return nil, err
	}
	if s.b.accountSlugTakenLocked(username) {
		return nil, fmt.Errorf("%w: username %q is not available", storage.ErrConflict, username)
	}

	createdBy := rec.invite.CreatedBy
	s.b.subjects[agentSubjectID] = storage.Subject{
		ID:                  agentSubjectID,
		Kind:                "agent",
		ControllerSubjectID: createdBy,
		DisplayName:         displayName,
	}
	s.b.provisionPersonalAccountLocked(agentSubjectID, username, displayName)
	s.b.addAccountRoleLocked(agentSubjectID, username, "owner")
	s.b.addAccountRoleLocked(createdBy, username, "owner")
	s.b.agentRegistrations[agentSubjectID] = agentRegistration{
		account:   username,
		claimedBy: createdBy,
		claimedAt: now,
		createdAt: now,
	}
	if rec.invite.SliceID != "" {
		if s.b.sliceMembers[rec.invite.SliceID] == nil {
			s.b.sliceMembers[rec.invite.SliceID] = map[string]memSliceMember{}
		}
		s.b.sliceMembers[rec.invite.SliceID][agentSubjectID] = memSliceMember{
			role:      rec.invite.SliceRole,
			addedBy:   createdBy,
			createdAt: now,
		}
	}
	s.b.addAPIKeyRecordLocked(storage.CreateAPIKeyInput{Name: "default"}, agentSubjectID, secret)
	rec.invite.ConsumedAt = now
	rec.consumedBy = agentSubjectID

	return &storage.JoinAgentInviteResult{
		AgentSubjectID: agentSubjectID,
		Account:        username,
		APIKey:         secret,
		SliceAccount:   rec.invite.SliceAccount,
		SliceSlug:      rec.invite.SliceSlug,
		SliceRole:      rec.invite.SliceRole,
	}, nil
}
