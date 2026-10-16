package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	id, _ := objectid.RandomID("key")
	hash := memoryTokenHash(secret)
	key := storage.APIKey{ID: id, SubjectID: subjectID, Name: name, CreatedAt: time.Now().UTC()}
	b.apiKeys[hash] = subjectID
	b.apiKeyRecords[id] = &memoryAPIKey{key: key, hash: hash}
	return key
}

// revokeAgentKeysLocked revokes every key the subject holds.
func (b *backend) revokeAgentKeysLocked(subjectID string) {
	for _, record := range b.apiKeyRecords {
		if record.key.SubjectID == subjectID && record.key.RevokedAt.IsZero() {
			record.key.RevokedAt = time.Now().UTC()
		}
	}
	for hash, owner := range b.apiKeys {
		if owner == subjectID {
			delete(b.apiKeys, hash)
		}
	}
}

// agentKeyTargetLocked mirrors the Postgres rule: the agent whose personal
// account is agentAccount ("" is the actor), managed by itself or an owner of
// its account. Anyone else gets ErrNotFound.
func (b *backend) agentKeyTargetLocked(actorID, agentAccount string) (subjectID, account string, err error) {
	actorID = strings.TrimSpace(actorID)
	agentAccount = strings.TrimSpace(agentAccount)
	if agentAccount != "" {
		if agentAccount = normalizeSlug(agentAccount); agentAccount == "" {
			return "", "", fmt.Errorf("%w: invalid agent account", storage.ErrInvalid)
		}
	}
	for id, reg := range b.agentRegistrations {
		if agentAccount == "" && id != actorID || agentAccount != "" && reg.account != agentAccount {
			continue
		}
		if id == actorID || b.accountMembers[actorID][reg.account] == "owner" {
			return id, reg.account, nil
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
	var keys []storage.APIKey
	for _, record := range s.b.apiKeyRecords {
		if record.key.SubjectID == subjectID {
			keys = append(keys, record.key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].CreatedAt.After(keys[j].CreatedAt)
		}
		return keys[i].ID < keys[j].ID
	})
	return subjectID, account, keys, nil
}

func (s *AuthStore) CreateAPIKey(ctx context.Context, actorID, agentAccount, name string) (storage.APIKey, string, string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "key"
	}
	if utf8.RuneCountInString(name) > storage.MaxAPIKeyNameLength {
		return storage.APIKey{}, "", "", "", fmt.Errorf("%w: key name must be %d characters or fewer", storage.ErrInvalid, storage.MaxAPIKeyNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return storage.APIKey{}, "", "", "", fmt.Errorf("%w: key name must not contain control characters", storage.ErrInvalid)
		}
	}
	secret, err := objectid.RandomID(strings.TrimSuffix(storage.APIKeyPrefix, "_"))
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	subjectID, account, err := s.b.agentKeyTargetLocked(actorID, agentAccount)
	if err != nil {
		return storage.APIKey{}, "", "", "", err
	}
	active := 0
	for _, record := range s.b.apiKeyRecords {
		if record.key.SubjectID == subjectID && record.key.Active(time.Now()) {
			active++
		}
	}
	if active >= storage.MaxActiveAPIKeys {
		return storage.APIKey{}, "", "", "", fmt.Errorf("%w: %s already has %d active API keys; revoke one first", storage.ErrConflict, account, active)
	}
	return s.b.addAPIKeyLocked(subjectID, name, secret), secret, subjectID, account, nil
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
	record := s.b.apiKeyRecords[keyID]
	if record == nil || record.key.SubjectID != subjectID {
		return storage.APIKey{}, fmt.Errorf("%w: no such API key for this agent", storage.ErrNotFound)
	}
	if record.key.RevokedAt.IsZero() {
		record.key.RevokedAt = time.Now().UTC()
	}
	delete(s.b.apiKeys, record.hash)
	return record.key, nil
}
