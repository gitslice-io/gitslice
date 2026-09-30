package storage

import (
	"fmt"
	"strings"
	"unicode"
)

const maxEmailBytes = 254

// NormalizeOwnerEmail trims and lowercases an agent owner email and applies a
// deliberately loose shape check: exactly one '@', a non-empty local part, a
// domain containing '.', no whitespace, and at most 254 bytes. It does not prove
// the address exists; ownership is only proven later by a verified sign-in.
func NormalizeOwnerEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", fmt.Errorf("%w: owner email is required", ErrInvalid)
	}
	if len(email) > maxEmailBytes {
		return "", fmt.Errorf("%w: owner email is too long", ErrInvalid)
	}
	if strings.IndexFunc(email, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%w: owner email must not contain whitespace", ErrInvalid)
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || strings.Contains(domain, "@") {
		return "", fmt.Errorf("%w: owner email %q is not a valid address", ErrInvalid, email)
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", fmt.Errorf("%w: owner email %q is not a valid address", ErrInvalid, email)
	}
	return email, nil
}
