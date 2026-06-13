package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ExternalUsername derives a stable, slug-safe username for a subject
// provisioned from an external identity provider (for example Clerk). The result
// is deterministic for a given externalID so repeated logins resolve to the same
// subject and personal account. The email, when present, is used only to make the
// username human-recognizable; the externalID hash guarantees uniqueness.
func ExternalUsername(externalID, email string) string {
	base := ""
	if at := strings.IndexByte(email, '@'); at > 0 {
		base = email[:at]
	}
	base = sanitizeUsernameBase(base)
	if base == "" {
		base = "user"
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(externalID)))
	suffix := hex.EncodeToString(sum[:])[:8]
	name := base + "-" + suffix
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func sanitizeUsernameBase(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '.' || r == '_' || r == '+':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
