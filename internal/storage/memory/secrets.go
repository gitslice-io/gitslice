package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
)

// memorySecret is a secret with its value (design/33_secrets.md).
type memorySecret struct {
	storage.Secret
	value string
}

func (s *SliceStore) CreateSecret(ctx context.Context, account, name, value string, secretPaths []string, actorID string) (*storage.Secret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	account = strings.TrimSpace(account)
	if !storage.ValidSliceSecretName(name) {
		return nil, fmt.Errorf("%w: secret name must match ^[A-Z_][A-Z0-9_]*$", storage.ErrInvalid)
	}
	normalized, err := storage.NormalizeSecretPaths(account, secretPaths)
	if err != nil {
		return nil, err
	}
	if len(value) > storage.MaxSecretValueBytes {
		return nil, fmt.Errorf("%w: a secret value can be at most %d KiB", storage.ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	if err := s.b.requireSecretRoomLocked(account); err != nil {
		return nil, err
	}
	id := s.b.nextIDLocked("sec")
	if err := s.b.secretPathConflictLocked(id, name, normalized); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s.b.secrets[id] = &memorySecret{
		Secret: storage.Secret{
			ID: id, Account: account, Name: name, Paths: normalized,
			CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		},
		value: value,
	}
	return s.b.secretLocked(id), nil
}

func (s *SliceStore) UpdateSecret(ctx context.Context, secretID string, value *string, secretPaths []string, actorID string) (*storage.Secret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	secret := s.b.secrets[strings.TrimSpace(secretID)]
	if secret == nil {
		return nil, storage.ErrNotFound
	}
	if value != nil && len(*value) > storage.MaxSecretValueBytes {
		return nil, fmt.Errorf("%w: a secret value can be at most %d KiB", storage.ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	var normalized []string
	if secretPaths != nil {
		var err error
		if normalized, err = storage.NormalizeSecretPaths(secret.Account, secretPaths); err != nil {
			return nil, err
		}
		if err := s.b.secretPathConflictLocked(secret.ID, secret.Name, normalized); err != nil {
			return nil, err
		}
	}
	if value != nil {
		secret.value = *value
	}
	if normalized != nil {
		secret.Paths = normalized
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
	return s.b.listSecretsLocked(strings.TrimSpace(account)), nil
}

func (s *SliceStore) SecretValuesAt(ctx context.Context, folder string) (map[string]string, error) {
	cleaned, err := paths.CanonicalPrefix(strings.TrimSpace(folder))
	if err != nil {
		return nil, fmt.Errorf("%w: folder %q: %v", storage.ErrInvalid, folder, err)
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	best := map[string]string{}
	out := map[string]string{}
	for _, secret := range s.b.secrets {
		at, ok := storage.SecretAppliesAt(secret.Paths, cleaned)
		if !ok {
			continue
		}
		if prev, seen := best[secret.Name]; !seen || len(at) > len(prev) {
			best[secret.Name] = at
			out[secret.Name] = secret.value
		}
	}
	return out, nil
}

func (s *SliceStore) ListSliceSecrets(ctx context.Context, sliceID string) ([]storage.SliceSecret, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	account, prefixes, err := s.b.secretSliceLocked(sliceID)
	if err != nil {
		return nil, err
	}
	var out []storage.SliceSecret
	for _, secret := range s.b.listSecretsLocked(account) {
		if storage.SecretTouches(secret.Paths, prefixes) {
			out = append(out, storage.SliceSecret{Name: secret.Name, SecretID: secret.ID, Paths: secret.Paths})
		}
	}
	return out, nil
}

func (s *SliceStore) SetSliceSecret(ctx context.Context, sliceID, name, value, actorID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if !storage.ValidSliceSecretName(name) {
		return storage.ErrInvalid
	}
	if len(value) > storage.MaxSecretValueBytes {
		return fmt.Errorf("%w: a secret value can be at most %d KiB", storage.ErrInvalid, storage.MaxSecretValueBytes>>10)
	}
	account, prefixes, err := s.b.secretSliceLocked(sliceID)
	if err != nil {
		return err
	}
	named := s.b.namedSecretsOnPathsLocked(name, prefixes)
	edit, err := storage.PlanSetSliceSecret(name, prefixes, named)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if edit.Create {
		if err := s.b.requireSecretRoomLocked(account); err != nil {
			return err
		}
		id := s.b.nextIDLocked("sec")
		s.b.secrets[id] = &memorySecret{
			Secret: storage.Secret{
				ID: id, Account: account, Name: name, Paths: append([]string(nil), prefixes...),
				CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
			},
			value: value,
		}
		return nil
	}
	for _, id := range edit.Update {
		secret := s.b.secrets[id]
		secret.value = value
		secret.UpdatedBy = actorID
		secret.UpdatedAt = now
	}
	if len(edit.AddPaths) > 0 {
		first := s.b.secrets[edit.Update[0]]
		first.Paths = append(first.Paths, edit.AddPaths...)
		sort.Strings(first.Paths)
	}
	return nil
}

func (s *SliceStore) DeleteSliceSecret(ctx context.Context, sliceID, name, actorID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if !storage.ValidSliceSecretName(name) {
		return storage.ErrInvalid
	}
	_, prefixes, err := s.b.secretSliceLocked(sliceID)
	if err != nil {
		return err
	}
	named := s.b.namedSecretsOnPathsLocked(name, prefixes)
	if len(named) == 0 {
		var otherID, otherPath string
		for _, secret := range s.b.secrets {
			if secret.Name != name {
				continue
			}
			for _, prefix := range prefixes {
				if at, ok := storage.SecretAppliesAt(secret.Paths, prefix); ok && len(at) > len(otherPath) {
					otherID, otherPath = secret.ID, at
				}
			}
		}
		if otherID != "" {
			return fmt.Errorf("%w: %s comes from secret %s on %s, a folder above this slice; delete it or change its folders with gs secret", storage.ErrConflict, name, otherID, otherPath)
		}
		return nil
	}
	for _, found := range named {
		if storage.PathsWithin(found.Paths, prefixes) {
			delete(s.b.secrets, found.ID)
			continue
		}
		secret := s.b.secrets[found.ID]
		var kept []string
		for _, p := range secret.Paths {
			if !storage.PathsWithin([]string{p}, prefixes) {
				kept = append(kept, p)
			}
		}
		secret.Paths = kept
		secret.UpdatedBy = actorID
		secret.UpdatedAt = time.Now().UTC()
	}
	return nil
}

// secretLocked returns a copy of the secret, or nil.
func (b *backend) secretLocked(id string) *storage.Secret {
	secret := b.secrets[id]
	if secret == nil {
		return nil
	}
	out := secret.Secret
	out.Paths = append([]string(nil), secret.Paths...)
	return &out
}

func (b *backend) listSecretsLocked(account string) []storage.Secret {
	var out []storage.Secret
	for id, secret := range b.secrets {
		if secret.Account == account {
			out = append(out, *b.secretLocked(id))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt) || (out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID)
	})
	return out
}

// secretSliceLocked returns a live slice's account and included paths.
func (b *backend) secretSliceLocked(sliceID string) (string, []string, error) {
	sliceID = strings.TrimSpace(sliceID)
	if sliceID == "" {
		return "", nil, fmt.Errorf("%w: slice_id is required", storage.ErrInvalid)
	}
	slice := b.slices[sliceID]
	if slice == nil || b.retiredSlices[sliceID] {
		return "", nil, storage.ErrNotFound
	}
	prefixes := append([]string(nil), slice.GetDefinition().GetIncludedPaths()...)
	if len(prefixes) == 0 {
		return "", nil, fmt.Errorf("%w: slice %s includes no paths", storage.ErrInvalid, sliceID)
	}
	sort.Strings(prefixes)
	return slice.GetRef().GetAccount(), prefixes, nil
}

// namedSecretsOnPathsLocked returns copies of the secrets named name with
// one of the paths.
func (b *backend) namedSecretsOnPathsLocked(name string, onPaths []string) []storage.Secret {
	var out []storage.Secret
	for id, secret := range b.secrets {
		if secret.Name != name {
			continue
		}
		for _, p := range secret.Paths {
			if storage.PathsWithin([]string{p}, onPaths) {
				out = append(out, *b.secretLocked(id))
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
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

// secretPathConflictLocked refuses paths that already have another secret
// named name.
func (b *backend) secretPathConflictLocked(secretID, name string, secretPaths []string) error {
	for _, other := range b.secrets {
		if other.ID == secretID || other.Name != name {
			continue
		}
		for _, p := range secretPaths {
			if storage.PathsWithin([]string{p}, other.Paths) {
				return fmt.Errorf("%w: %s already has secret %s named %s", storage.ErrConflict, p, other.ID, name)
			}
		}
	}
	return nil
}
