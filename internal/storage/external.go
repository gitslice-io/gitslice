package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ExternalSubjectID returns the stable internal subject id for an external
// identity. It depends ONLY on the externalID (the identity provider's immutable
// user id, e.g. a Clerk user id) — never the email or other mutable profile
// data — so a subject resolves to the same id on every request even if their
// email changes or is absent from a particular token. Deriving identity from the
// email base previously made the subject id flip between logins, orphaning the
// subject's account.
func ExternalSubjectID(externalID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(externalID)))
	return "user_ext_" + hex.EncodeToString(sum[:])[:16]
}
