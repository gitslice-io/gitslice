package memory

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// memoryAPIKey is an API key's record. The key authenticates through
// backend.apiKeys, under hash, until it is revoked.
type memoryAPIKey struct {
	key  storage.APIKey
	hash string
}

// addAPIKeyLocked records a new key for the subject and makes the secret
// authenticate as it.
func (b *backend) addAPIKeyLocked(subjectID, name, secret string) storage.APIKey {
	return b.addAPIKeyRecordLocked(storage.CreateAPIKeyInput{Name: name}, subjectID, secret)
}

func (b *backend) addAPIKeyRecordLocked(in storage.CreateAPIKeyInput, subjectID, secret string) storage.APIKey {
	id, _ := objectid.RandomID("key")
	hash := memoryTokenHash(secret)
	var sliceIDs []string
	if in.HasSliceScope {
		sliceIDs = slices.Clone(in.SliceIDs)
		if sliceIDs == nil {
			sliceIDs = []string{}
		}
	}
	key := storage.APIKey{
		ID:             id,
		SubjectID:      subjectID,
		Name:           in.Name,
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      in.ExpiresAt,
		HasSliceScope:  in.HasSliceScope,
		SliceIDs:       sliceIDs,
		MaxRole:        in.MaxRole,
		ParentKeyID:    in.ParentKeyID,
		Purpose:        in.Purpose,
		DaemonID:       in.DaemonID,
		ConversationID: in.ConversationID,
	}
	b.apiKeys[hash] = subjectID
	b.apiKeyRecords[id] = &memoryAPIKey{key: key, hash: hash}
	return cloneAPIKey(key)
}

func cloneAPIKey(k storage.APIKey) storage.APIKey {
	k.SliceIDs = slices.Clone(k.SliceIDs)
	return k
}

// apiKeyChainActiveLocked checks that keyID and every ancestor in its
// ParentKeyID chain exist, are active at now, and contain no cycles.
func (b *backend) apiKeyChainActiveLocked(keyID string, now time.Time) bool {
	visited := make(map[string]bool)
	cur := keyID
	for cur != "" {
		if visited[cur] {
			return false
		}
		visited[cur] = true
		rec := b.apiKeyRecords[cur]
		if rec == nil || !rec.key.Active(now) {
			return false
		}
		cur = rec.key.ParentKeyID
	}
	return len(visited) > 0
}

// revokeKeyCascadeLocked revokes keyID and all transitive child keys, keeping
// any existing revocation timestamp.
func (b *backend) revokeKeyCascadeLocked(keyID string, now time.Time) {
	queue := []string{keyID}
	visited := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		rec := b.apiKeyRecords[cur]
		if rec != nil {
			if rec.key.RevokedAt.IsZero() {
				rec.key.RevokedAt = now
			}
			delete(b.apiKeys, rec.hash)
		}
		for _, candidate := range b.apiKeyRecords {
			if candidate.key.ParentKeyID == cur && !visited[candidate.key.ID] {
				queue = append(queue, candidate.key.ID)
			}
		}
	}
}

// revokeAgentKeysLocked revokes every key the subject holds.
func (b *backend) revokeAgentKeysLocked(subjectID string) {
	now := time.Now().UTC()
	for _, record := range b.apiKeyRecords {
		if record.key.SubjectID == subjectID {
			b.revokeKeyCascadeLocked(record.key.ID, now)
		}
	}
}

// agentKeyTargetLocked mirrors the Postgres rule:
//   - agentAccount == "": the actor itself (user or agent).
//   - agentAccount != "": the agent whose personal account is agentAccount,
//     managed by itself, its controller, or an owner of its account.
func (b *backend) agentKeyTargetLocked(actorID, agentAccount string) (subjectID, account string, err error) {
	actorID = strings.TrimSpace(actorID)
	agentAccount = strings.TrimSpace(agentAccount)
	if agentAccount == "" {
		if actorID == "" {
			return "", "", storage.ErrUnauthenticated
		}
		if _, ok := b.subjects[actorID]; !ok {
			return "", "", fmt.Errorf("%w: subject not found", storage.ErrNotFound)
		}
		return actorID, b.personalAccounts[actorID], nil
	}
	if agentAccount = normalizeSlug(agentAccount); agentAccount == "" {
		return "", "", fmt.Errorf("%w: invalid agent account", storage.ErrInvalid)
	}
	for id, subj := range b.subjects {
		if subj.Kind != "agent" {
			continue
		}
		acct := b.personalAccounts[id]
		if acct == "" {
			if reg, ok := b.agentRegistrations[id]; ok {
				acct = reg.account
			}
		}
		if acct != agentAccount {
			continue
		}
		if id == actorID || subj.ControllerSubjectID == actorID || b.accountMembers[actorID][acct] == "owner" {
			return id, acct, nil
		}
	}
	return "", "", fmt.Errorf("%w: no agent account you own or are signed in as", storage.ErrNotFound)
}

func (s *AuthStore) ListAPIKeys(ctx context.Context, actorID, agentAccount string) (string, string, []storage.APIKey, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID, account, err := s.b.agentKeyTargetLocked(actorID, agentAccount)
	if err != nil {
		return "", "", nil, err
	}
	principal, hasPrincipal := authctx.PrincipalFrom(ctx)
	if hasPrincipal && principal.KeyID != "" && subjectID != principal.SubjectID {
		return "", "", nil, fmt.Errorf("%w: an API key cannot list keys for another subject", storage.ErrUnauthorized)
	}
	var keys []storage.APIKey
	for _, record := range s.b.apiKeyRecords {
		if record.key.SubjectID == subjectID {
			keys = append(keys, cloneAPIKey(record.key))
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].CreatedAt.After(keys[j].CreatedAt)
		}
		return keys[i].ID < keys[j].ID
	})
	if hasPrincipal && principal.KeyID != "" {
		keys = storage.FilterAPIKeysForCallerKey(keys, principal.KeyID)
	}
	return subjectID, account, keys, nil
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
	secret, err := objectid.RandomID(strings.TrimSuffix(storage.APIKeyPrefix, "_"))
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID, account, err := s.b.agentKeyTargetLocked(normalized.ActorID, normalized.AgentAccount)
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	if principal, ok := authctx.PrincipalFrom(ctx); ok && principal.KeyID != "" && subjectID != principal.SubjectID {
		return storage.APIKey{}, "", "", "", fmt.Errorf("%w: an API key cannot create keys for another subject", storage.ErrUnauthorized)
	}
	if normalized.ParentKeyID != "" {
		parentRec := s.b.apiKeyRecords[normalized.ParentKeyID]
		if parentRec == nil || !s.b.apiKeyChainActiveLocked(normalized.ParentKeyID, now) {
			return storage.APIKey{}, "", "", "", fmt.Errorf("%w: parent API key is revoked or expired", storage.ErrUnauthenticated)
		}
		if parentRec.key.SubjectID != subjectID {
			return storage.APIKey{}, "", "", "", fmt.Errorf("%w: an API key cannot create keys for another subject", storage.ErrUnauthorized)
		}
		normalized, err = storage.AttenuateAPIKeyInput(parentRec.key, normalized, now)
		if err != nil {
			return storage.APIKey{}, "", "", "", err
		}
	}
	active := 0
	for _, record := range s.b.apiKeyRecords {
		if record.key.SubjectID == subjectID && record.key.Active(now) {
			active++
		}
	}
	if active >= storage.MaxActiveAPIKeys {
		label := account
		if label == "" {
			label = subjectID
		}
		return storage.APIKey{}, "", "", "", fmt.Errorf("%w: %s already has %d active API keys; revoke one first", storage.ErrConflict, label, active)
	}
	return s.b.addAPIKeyRecordLocked(normalized, subjectID, secret), secret, subjectID, account, nil
}

func (s *AuthStore) RevokeAPIKey(ctx context.Context, actorID, agentAccount, keyID string) (storage.APIKey, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return storage.APIKey{}, fmt.Errorf("%w: key id is required", storage.ErrInvalid)
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID, _, err := s.b.agentKeyTargetLocked(actorID, agentAccount)
	if err != nil {
		return storage.APIKey{}, err
	}
	if principal, ok := authctx.PrincipalFrom(ctx); ok && principal.KeyID != "" {
		if subjectID != principal.SubjectID {
			return storage.APIKey{}, fmt.Errorf("%w: an API key cannot revoke keys for another subject", storage.ErrUnauthorized)
		}
		callerRec := s.b.apiKeyRecords[principal.KeyID]
		if callerRec == nil || callerRec.key.SubjectID != subjectID {
			return storage.APIKey{}, storage.ErrUnauthenticated
		}
		targetRec := s.b.apiKeyRecords[keyID]
		if targetRec == nil || targetRec.key.SubjectID != subjectID {
			return storage.APIKey{}, fmt.Errorf("%w: no such API key for this agent", storage.ErrNotFound)
		}
		if callerRec.key.HasSliceScope || callerRec.key.MaxRole != "" || callerRec.key.ParentKeyID != "" {
			if !s.b.isSelfOrDescendantKeyLocked(principal.KeyID, keyID) {
				return storage.APIKey{}, fmt.Errorf("%w: an attenuated or child API key can only revoke itself or its descendants", storage.ErrUnauthorized)
			}
		}
	}
	record := s.b.apiKeyRecords[keyID]
	if record == nil || record.key.SubjectID != subjectID {
		return storage.APIKey{}, fmt.Errorf("%w: no such API key for this agent", storage.ErrNotFound)
	}
	s.b.revokeKeyCascadeLocked(keyID, time.Now().UTC())
	return cloneAPIKey(record.key), nil
}

func (b *backend) isSelfOrDescendantKeyLocked(ancestorID, candidateID string) bool {
	if ancestorID == candidateID {
		return true
	}
	visited := map[string]bool{}
	cur := candidateID
	for cur != "" {
		if visited[cur] {
			return false
		}
		visited[cur] = true
		rec := b.apiKeyRecords[cur]
		if rec == nil {
			return false
		}
		if rec.key.ParentKeyID == ancestorID {
			return true
		}
		cur = rec.key.ParentKeyID
	}
	return false
}
