package storage

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/paths"
)

// Secret limits (design/33_secrets.md).
const (
	MaxSecretValueBytes  = 48 << 10
	MaxSecretsPerAccount = 200
	MaxSecretPaths       = 50
)

// Secret is a value an account keeps for the CI checks of some of its
// folders (design/33_secrets.md). Reads never carry the value.
type Secret struct {
	ID string
	// Account is the owning account's name.
	Account string
	Name    string
	// Paths are the canonical folders it applies to, with everything below
	// them, sorted. Each is inside /<Account>.
	Paths []string
	// Subject ids.
	CreatedBy string
	UpdatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SliceSecret is a secret that applies somewhere in a slice, and the folders
// it is scoped to.
type SliceSecret struct {
	Name     string
	SecretID string
	Paths    []string
}

// NormalizeSecretPaths cleans a secret's folders: each becomes canonical
// (/acme/services/billing), must lie inside /<account>, and repeats go. The
// result is sorted. At least one path and at most MaxSecretPaths.
func NormalizeSecretPaths(account string, in []string) ([]string, error) {
	account = strings.TrimSpace(account)
	root := "/" + account
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		cleaned, err := paths.CanonicalPrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: secret path %q: %v", ErrInvalid, raw, err)
		}
		if !paths.Contains(root, cleaned) {
			return nil, fmt.Errorf("%w: secret path %s is outside account %s; it must be %s or a folder under it", ErrInvalid, cleaned, account, root)
		}
		if !seen[cleaned] {
			seen[cleaned] = true
			out = append(out, cleaned)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: a secret applies to at least one folder", ErrInvalid)
	}
	if len(out) > MaxSecretPaths {
		return nil, fmt.Errorf("%w: a secret can apply to at most %d folders", ErrInvalid, MaxSecretPaths)
	}
	sort.Strings(out)
	return out, nil
}

// SecretAppliesAt returns the longest of a secret's paths that covers folder
// (the folder itself or an ancestor), and whether one does. Among secrets of
// one name, the one with the longest such path wins at that folder.
func SecretAppliesAt(secretPaths []string, folder string) (string, bool) {
	best, found := "", false
	for _, p := range secretPaths {
		if paths.Contains(p, folder) && len(p) > len(best) {
			best, found = p, true
		}
	}
	return best, found
}

// SecretTouches reports whether a secret applies anywhere under one of the
// prefixes: one of its paths covers a prefix or lies inside one.
func SecretTouches(secretPaths, prefixes []string) bool {
	for _, p := range secretPaths {
		for _, prefix := range prefixes {
			if paths.Contains(p, prefix) || paths.Contains(prefix, p) {
				return true
			}
		}
	}
	return false
}

// SecretFolderAncestors returns folder and each folder above it, the paths
// whose secrets apply at folder.
func SecretFolderAncestors(folder string) []string {
	return paths.AncestorPrefixes(folder)
}

// PathsWithin reports whether every one of secretPaths is one of prefixes.
func PathsWithin(secretPaths, prefixes []string) bool {
	for _, p := range secretPaths {
		found := false
		for _, prefix := range prefixes {
			if p == prefix {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// SliceSecretEdit is what SetSliceSecret does: create a secret on the slice's
// paths, or give the secrets in Update the new value, adding AddPaths to the
// first of them.
type SliceSecretEdit struct {
	Create   bool
	Update   []string
	AddPaths []string
}

// PlanSetSliceSecret decides how SetSliceSecret gives name a new value on a
// slice's included paths (slicePaths). named holds the secrets of that name
// on one of those paths, with all their paths. A secret that also applies
// outside the slice's paths is never changed from here: ErrConflict.
func PlanSetSliceSecret(name string, slicePaths []string, named []Secret) (SliceSecretEdit, error) {
	if len(named) == 0 {
		return SliceSecretEdit{Create: true}, nil
	}
	covered := map[string]bool{}
	var edit SliceSecretEdit
	for _, secret := range named {
		if !PathsWithin(secret.Paths, slicePaths) {
			var others []string
			for _, p := range secret.Paths {
				if !PathsWithin([]string{p}, slicePaths) {
					others = append(others, p)
				}
			}
			return SliceSecretEdit{}, fmt.Errorf("%w: %s on this slice comes from secret %s, which also applies to %s; change it there with gs secret, or take this slice's folders out of it first", ErrConflict, name, secret.ID, strings.Join(others, ", "))
		}
		edit.Update = append(edit.Update, secret.ID)
		for _, p := range secret.Paths {
			covered[p] = true
		}
	}
	for _, p := range slicePaths {
		if !covered[p] {
			edit.AddPaths = append(edit.AddPaths, p)
		}
	}
	return edit, nil
}
