package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

// memorySecret is a secret with its value (design/33_secrets.md).
type memorySecret struct {
	storage.Secret
	value    string
	sliceIDs map[string]bool
}

func (s *SliceStore) CreateSecret(ctx context.Context, account, name, value string, scope storage.SecretScope, actorID string) (*storage.Secret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	account = strings.TrimSpace(account)
	if !storage.ValidSliceSecretName(name) {
		return nil, fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", storage.ErrInvalid)
	}
	if len(value) > storage.MaxSecretValueBytes {
		return nil, fmt.Errorf("%w: a secret value can be at most %d KiB", storage.ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	if err := s.b.requireSecretRoomLocked(account); err != nil {
		return nil, err
	}
	sliceIDs, err := s.b.secretScopeSlicesLocked(account, scope)
	if err != nil {
		return nil, err
	}
	id := s.b.nextIDLocked("sec")
	if err := s.b.secretConflictLocked(account, id, name, scope.AllSlices, sliceIDs); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s.b.secrets[id] = &memorySecret{
		Secret: storage.Secret{
			ID: id, Account: account, Name: name, AllSlices: scope.AllSlices,
			CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		},
		value:    value,
		sliceIDs: sliceIDs,
	}
	return s.b.secretLocked(id), nil
}

func (s *SliceStore) UpdateSecret(ctx context.Context, secretID string, value *string, scope *storage.SecretScope, actorID string) (*storage.Secret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	secret := s.b.secrets[strings.TrimSpace(secretID)]
	if secret == nil {
		return nil, storage.ErrNotFound
	}
	if value != nil && len(*value) > storage.MaxSecretValueBytes {
		return nil, fmt.Errorf("%w: a secret value can be at most %d KiB", storage.ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	var sliceIDs map[string]bool
	if scope != nil {
		var err error
		if sliceIDs, err = s.b.secretScopeSlicesLocked(secret.Account, *scope); err != nil {
			return nil, err
		}
		if err := s.b.secretConflictLocked(secret.Account, secret.ID, secret.Name, scope.AllSlices, sliceIDs); err != nil {
			return nil, err
		}
	}
	if value != nil {
		secret.value = *value
	}
	if scope != nil {
		secret.AllSlices = scope.AllSlices
		secret.sliceIDs = sliceIDs
	}
	secret.UpdatedBy = actorID
	secret.UpdatedAt = time.Now().UTC()
	return s.b.secretLocked(secret.ID), nil
}

func (s *SliceStore) DeleteSecret(ctx context.Context, secretID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	secretID = strings.TrimSpace(secretID)
	if s.b.secrets[secretID] == nil {
		return storage.ErrNotFound
	}
	delete(s.b.secrets, secretID)
	return nil
}

func (s *SliceStore) GetSecret(ctx context.Context, secretID string) (*storage.Secret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	secret := s.b.secretLocked(strings.TrimSpace(secretID))
	if secret == nil {
		return nil, storage.ErrNotFound
	}
	return secret, nil
}

func (s *SliceStore) ListSecrets(ctx context.Context, account string) ([]storage.Secret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	account = strings.TrimSpace(account)
	var out []storage.Secret
	for id, secret := range s.b.secrets {
		if secret.Account == account {
			out = append(out, *s.b.secretLocked(id))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt) || (out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID)
	})
	return out, nil
}

func (s *SliceStore) ListSliceSecrets(ctx context.Context, sliceID string) ([]storage.SliceSecret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	reaching, err := s.b.sliceSecretsLocked(sliceID)
	if err != nil {
		return nil, err
	}
	out := make([]storage.SliceSecret, 0, len(reaching))
	for _, secret := range reaching {
		item := storage.SliceSecret{Name: secret.Name, SecretID: secret.ID, AllSlices: secret.AllSlices}
		for _, slice := range s.b.secretLocked(secret.ID).Slices {
			if slice.ID != sliceID {
				item.SharedWith = append(item.SharedWith, slice)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *SliceStore) ListSliceSecretNames(ctx context.Context, sliceID string) ([]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	reaching, err := s.b.sliceSecretsLocked(sliceID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(reaching))
	for _, secret := range reaching {
		names = append(names, secret.Name)
	}
	return names, nil
}

func (s *SliceStore) GetSliceSecrets(ctx context.Context, sliceID string) (map[string]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	reaching, err := s.b.sliceSecretsLocked(sliceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(reaching))
	for _, secret := range reaching {
		out[secret.Name] = secret.value
	}
	return out, nil
}

func (s *SliceStore) SetSliceSecret(ctx context.Context, sliceID, name, value, actorID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" || !storage.ValidSliceSecretName(name) {
		return storage.ErrInvalid
	}
	if len(value) > storage.MaxSecretValueBytes {
		return fmt.Errorf("%w: a secret value can be at most %d KiB", storage.ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	slice := s.b.slices[sliceID]
	if slice == nil || s.b.retiredSlices[sliceID] {
		return storage.ErrNotFound
	}
	now := time.Now().UTC()
	if secret := s.b.listingSecretLocked(sliceID, name); secret != nil {
		if len(secret.sliceIDs) > 1 {
			return fmt.Errorf("%w: %s on this slice comes from secret %s, shared with %d other slice(s); change it there, or take this slice out of its scope first", storage.ErrConflict, name, secret.ID, len(secret.sliceIDs)-1)
		}
		secret.value = value
		secret.UpdatedBy = actorID
		secret.UpdatedAt = now
		return nil
	}
	account := slice.GetRef().GetAccount()
	if err := s.b.requireSecretRoomLocked(account); err != nil {
		return err
	}
	id := s.b.nextIDLocked("sec")
	s.b.secrets[id] = &memorySecret{
		Secret: storage.Secret{
			ID: id, Account: account, Name: name,
			CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		},
		value:    value,
		sliceIDs: map[string]bool{sliceID: true},
	}
	return nil
}

func (s *SliceStore) DeleteSliceSecret(ctx context.Context, sliceID, name, actorID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" || !storage.ValidSliceSecretName(name) {
		return storage.ErrInvalid
	}
	slice := s.b.slices[sliceID]
	if slice == nil || s.b.retiredSlices[sliceID] {
		return storage.ErrNotFound
	}
	if secret := s.b.listingSecretLocked(sliceID, name); secret != nil {
		if len(secret.sliceIDs) == 1 {
			delete(s.b.secrets, secret.ID)
			return nil
		}
		delete(secret.sliceIDs, sliceID)
		secret.UpdatedBy = actorID
		secret.UpdatedAt = time.Now().UTC()
		return nil
	}
	for _, secret := range s.b.secrets {
		if secret.AllSlices && secret.Name == name && secret.Account == slice.GetRef().GetAccount() {
			return fmt.Errorf("%w: %s comes from secret %s, which applies to every slice of the account; delete it or change its scope with gs secret", storage.ErrConflict, name, secret.ID)
		}
	}
	return nil
}

// secretLocked returns a copy of the secret with its slices, or nil.
func (b *backend) secretLocked(id string) *storage.Secret {
	secret := b.secrets[id]
	if secret == nil {
		return nil
	}
	out := secret.Secret
	out.Slices = nil
	for sliceID := range secret.sliceIDs {
		if slice := b.slices[sliceID]; slice != nil && !b.retiredSlices[sliceID] {
			out.Slices = append(out.Slices, storage.SecretSlice{ID: sliceID, Name: slice.GetRef().GetSlice()})
		}
	}
	sort.Slice(out.Slices, func(i, j int) bool { return out.Slices[i].Name < out.Slices[j].Name })
	return &out
}

// sliceSecretsLocked returns the secrets that reach the slice, by name.
func (b *backend) sliceSecretsLocked(sliceID string) ([]*memorySecret, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return nil, storage.ErrInvalid
	}
	slice := b.slices[sliceID]
	if slice == nil {
		return nil, storage.ErrNotFound
	}
	byName := map[string]*memorySecret{}
	for _, secret := range b.secrets {
		if secret.sliceIDs[sliceID] {
			byName[secret.Name] = secret
		}
	}
	for _, secret := range b.secrets {
		if secret.AllSlices && secret.Account == slice.GetRef().GetAccount() && byName[secret.Name] == nil {
			byName[secret.Name] = secret
		}
	}
	out := make([]*memorySecret, 0, len(byName))
	for _, secret := range byName {
		out = append(out, secret)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (b *backend) listingSecretLocked(sliceID, name string) *memorySecret {
	for _, secret := range b.secrets {
		if !secret.AllSlices && secret.Name == name && secret.sliceIDs[sliceID] {
			return secret
		}
	}
	return nil
}

func (b *backend) requireSecretRoomLocked(account string) error {
	count := 0
	for _, secret := range b.secrets {
		if secret.Account == account {
			count++
		}
	}
	if count >= storage.MaxSecretsPerAccount {
		return fmt.Errorf("%w: an account can have at most %d secrets", storage.ErrConflict, storage.MaxSecretsPerAccount)
	}
	return nil
}

func (b *backend) secretScopeSlicesLocked(account string, scope storage.SecretScope) (map[string]bool, error) {
	if scope.AllSlices {
		return nil, nil
	}
	ids := map[string]bool{}
	for _, id := range scope.SliceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		slice := b.slices[id]
		if slice == nil || b.retiredSlices[id] || slice.GetRef().GetAccount() != account {
			return nil, fmt.Errorf("%w: slice %s is not a slice of account %s", storage.ErrInvalid, id, account)
		}
		ids[id] = true
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: a secret applies to all slices or to at least one slice", storage.ErrInvalid)
	}
	return ids, nil
}

func (b *backend) secretConflictLocked(account, secretID, name string, allSlices bool, sliceIDs map[string]bool) error {
	for _, other := range b.secrets {
		if other.ID == secretID || other.Account != account || other.Name != name {
			continue
		}
		if allSlices && other.AllSlices {
			return fmt.Errorf("%w: secret %s already gives %s to every slice of the account", storage.ErrConflict, other.ID, name)
		}
		for sliceID := range sliceIDs {
			if other.sliceIDs[sliceID] {
				return fmt.Errorf("%w: slice %s already has secret %s named %s", storage.ErrConflict, b.slices[sliceID].GetRef().GetSlice(), other.ID, name)
			}
		}
	}
	return nil
}

// unscopeSecretsLocked takes a deleted or retired slice out of every secret's
// scope; a secret left with no slices stays.
func (b *backend) unscopeSecretsLocked(sliceID string) {
	for _, secret := range b.secrets {
		delete(secret.sliceIDs, sliceID)
	}
}
