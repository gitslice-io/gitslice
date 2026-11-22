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

func (s *AuthStore) ListPendingClaims(ctx context.Context, subjectID string) ([]storage.PendingClaim, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID = strings.TrimSpace(subjectID)
	emails := s.b.verifiedEmails[subjectID]
	var out []storage.PendingClaim
	for agentID, reg := range s.b.agentRegistrations {
		if reg.claimedBy != "" || agentID == subjectID {
			continue
		}
		if _, ok := emails[reg.ownerEmail]; !ok {
			continue
		}
		out = append(out, storage.PendingClaim{
			AgentSubjectID:        agentID,
			AgentDisplayName:      s.b.subjects[agentID].DisplayName,
			Account:               reg.account,
			OwnerEmail:            reg.ownerEmail,
			CreatedAt:             reg.createdAt,
			RequestedSliceAccount: reg.requestedSliceAccount,
			RequestedSliceSlug:    reg.requestedSliceSlug,
			RequestedSliceRole:    reg.requestedSliceRole,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].AgentSubjectID < out[j].AgentSubjectID
	})
	return out, nil
}

func (s *AuthStore) AcceptClaim(ctx context.Context, subjectID, agentSubjectID string) (string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID = strings.TrimSpace(subjectID)
	agentSubjectID = strings.TrimSpace(agentSubjectID)
	reg, ok := s.b.agentRegistrations[agentSubjectID]
	if !ok || reg.claimedBy != "" || subjectID == "" || subjectID == agentSubjectID {
		return "", fmt.Errorf("%w: no pending claim for this agent", storage.ErrNotFound)
	}
	if _, verified := s.b.verifiedEmails[subjectID][reg.ownerEmail]; !verified {
		return "", fmt.Errorf("%w: no pending claim for this agent", storage.ErrNotFound)
	}
	s.b.addAccountRoleLocked(subjectID, reg.account, "owner")
	reg.claimedBy = subjectID
	reg.claimedAt = time.Now().UTC()
	s.b.agentRegistrations[agentSubjectID] = reg
	if subj, ok := s.b.subjects[agentSubjectID]; ok {
		subj.ControllerSubjectID = subjectID
		s.b.subjects[agentSubjectID] = subj
	}
	return reg.account, nil
}

func (s *AuthStore) RegisterAgent(ctx context.Context, in storage.RegisterAgentInput) (*storage.RegisteredAgent, error) {
	username, err := normalizeSignupUsername(in.Username)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", storage.ErrInvalid, err)
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
	token, err := objectid.RandomID(strings.TrimSuffix(storage.APIKeyPrefix, "_"))
	if err != nil {
		return nil, err
	}

	registrationToken := strings.TrimSpace(in.RegistrationToken)
	if registrationToken != "" && len(registrationToken) < storage.MinRegistrationTokenLength {
		return nil, fmt.Errorf("%w: registration token must be at least %d characters", storage.ErrInvalid, storage.MinRegistrationTokenLength)
	}

	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if registrationToken != "" {
		for agentID, reg := range s.b.agentRegistrations {
			if reg.registrationTokenHash != memoryTokenHash(registrationToken) {
				continue
			}
			if reg.account != username || reg.ownerEmail != ownerEmail {
				return nil, fmt.Errorf("%w: registration token was already used for a different agent", storage.ErrConflict)
			}
			s.b.revokeUnusedAgentKeysLocked(agentID)
			s.b.addAPIKeyLocked(agentID, "default", token)
			return &storage.RegisteredAgent{SubjectID: agentID, Account: reg.account, APIKey: token, Resumed: true}, nil
		}
	}
	if s.b.accountSlugTakenLocked(username) {
		return nil, fmt.Errorf("%w: username %q is not available", storage.ErrConflict, username)
	}
	s.b.subjects[subjectID] = storage.Subject{
		ID:          subjectID,
		Kind:        "agent",
		DisplayName: displayName,
	}
	s.b.provisionPersonalAccountLocked(subjectID, username, displayName)
	reg := agentRegistration{
		account:               username,
		ownerEmail:            ownerEmail,
		createdAt:             time.Now().UTC(),
		requestedSliceAccount: strings.TrimSpace(in.RequestedSliceAccount),
		requestedSliceSlug:    strings.TrimSpace(in.RequestedSliceSlug),
		requestedSliceRole:    strings.TrimSpace(in.RequestedSliceRole),
	}
	if registrationToken != "" {
		reg.registrationTokenHash = memoryTokenHash(registrationToken)
	}
	s.b.agentRegistrations[subjectID] = reg
	s.b.addAPIKeyLocked(subjectID, "default", token)
	return &storage.RegisteredAgent{SubjectID: subjectID, Account: username, APIKey: token}, nil
}

func (s *AuthStore) CreateDelegatedAgent(ctx context.Context, in storage.CreateDelegatedAgentInput) (*storage.CreateDelegatedAgentResult, error) {
	actorID := strings.TrimSpace(in.ActorID)
	if actorID == "" {
		return nil, storage.ErrUnauthenticated
	}
	username, err := normalizeSignupUsername(in.Username)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", storage.ErrInvalid, err)
	}
	displayName, err := storage.NormalizeDelegatedAgentDisplayName(in.DisplayName, username)
	if err != nil {
		return nil, err
	}
	sliceID := strings.TrimSpace(in.SliceID)
	sliceRole, err := storage.NormalizeDelegatedAgentSliceRole(sliceID, in.SliceRole)
	if err != nil {
		return nil, err
	}
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
	actor, ok := s.b.subjects[actorID]
	if !ok {
		return nil, storage.ErrUnauthenticated
	}
	if actor.Kind != "user" {
		return nil, fmt.Errorf("%w: only human users can create delegated agents", storage.ErrUnauthorized)
	}
	if s.b.accountSlugTakenLocked(username) {
		return nil, fmt.Errorf("%w: username %q is not available", storage.ErrConflict, username)
	}
	s.b.subjects[agentSubjectID] = storage.Subject{
		ID:                  agentSubjectID,
		Kind:                "agent",
		ControllerSubjectID: actorID,
		DisplayName:         displayName,
	}
	s.b.provisionPersonalAccountLocked(agentSubjectID, username, displayName)
	s.b.addAccountRoleLocked(agentSubjectID, username, "owner")
	s.b.addAccountRoleLocked(actorID, username, "owner")
	now := time.Now().UTC()
	s.b.agentRegistrations[agentSubjectID] = agentRegistration{
		account:   username,
		claimedBy: actorID,
		claimedAt: now,
		createdAt: now,
	}
	if sliceID != "" {
		if s.b.sliceMembers[sliceID] == nil {
			s.b.sliceMembers[sliceID] = map[string]memSliceMember{}
		}
		s.b.sliceMembers[sliceID][agentSubjectID] = memSliceMember{
			role:      sliceRole,
			addedBy:   actorID,
			createdAt: now,
		}
	}
	key := s.b.addAPIKeyRecordLocked(storage.CreateAPIKeyInput{Name: "default"}, agentSubjectID, secret)
	return &storage.CreateDelegatedAgentResult{
		AgentSubjectID: agentSubjectID,
		Account:        username,
		Key:            key,
		Secret:         secret,
		Role:           sliceRole,
	}, nil
}

func (s *AuthStore) ListOwnedAgents(ctx context.Context, subjectID string) ([]storage.OwnedAgent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID = strings.TrimSpace(subjectID)
	var out []storage.OwnedAgent
	for agentID, reg := range s.b.agentRegistrations {
		if agentID == subjectID || s.b.accountMembers[subjectID][reg.account] != "owner" {
			continue
		}
		var lastActiveAt time.Time
		for _, rec := range s.b.apiKeyRecords {
			if rec.key.SubjectID == agentID && rec.key.LastUsedAt.After(lastActiveAt) {
				lastActiveAt = rec.key.LastUsedAt
			}
		}
		out = append(out, storage.OwnedAgent{
			AgentSubjectID:   agentID,
			AgentDisplayName: s.b.subjects[agentID].DisplayName,
			Account:          reg.account,
			RegisteredAt:     reg.createdAt,
			ClaimedAt:        reg.claimedAt,
			LastActiveAt:     lastActiveAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RegisteredAt.Equal(out[j].RegisteredAt) {
			return out[i].RegisteredAt.Before(out[j].RegisteredAt)
		}
		return out[i].AgentSubjectID < out[j].AgentSubjectID
	})
	return out, nil
}
