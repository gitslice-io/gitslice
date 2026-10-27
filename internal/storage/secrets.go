package storage

import "time"

// Secret limits (design/33_secrets.md).
const (
	MaxSecretValueBytes  = 48 << 10
	MaxSecretsPerAccount = 200
)

// Secret is a value an account keeps for the CI checks of some of its slices
// (design/33_secrets.md). Reads never carry the value.
type Secret struct {
	ID string
	// Account is the owning account's name.
	Account string
	Name    string
	// AllSlices: it applies to every slice of the account. Otherwise it
	// applies to Slices.
	AllSlices bool
	Slices    []SecretSlice
	// Subject ids.
	CreatedBy string
	UpdatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SecretSlice is a slice a secret is scoped to.
type SecretSlice struct {
	ID string
	// Name is the slice's name in the secret's account.
	Name string
}

// SecretScope is where a secret applies: every slice of its account, or the
// slices listed by id. The slices must belong to the secret's account.
type SecretScope struct {
	AllSlices bool
	SliceIDs  []string
}

// SliceSecret is a secret that reaches one slice, and where it comes from.
type SliceSecret struct {
	Name      string
	SecretID  string
	AllSlices bool
	// SharedWith lists the other slices it is scoped to, when not AllSlices.
	SharedWith []SecretSlice
}
