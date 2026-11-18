package storage

import (
	"sort"

	"gitslice.io/gitslice/internal/model"
)

// NormalizeOwnerEmail trims and lowercases an agent owner email and applies a
// deliberately loose shape check: exactly one '@', a non-empty local part, a
// domain containing '.', no whitespace, and at most 254 bytes. It does not prove
// the address exists; ownership is only proven later by a verified sign-in.
func NormalizeOwnerEmail(email string) (string, error) {
	return model.NormalizeOwnerEmail(email)
}

// NormalizeEmails normalizes each address with NormalizeOwnerEmail, dropping
// malformed ones and duplicates, and returns them sorted.
func NormalizeEmails(emails []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(emails))
	for _, email := range emails {
		normalized, err := NormalizeOwnerEmail(email)
		if err != nil {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}
