package memory

import (
	"path"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/usernames"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func validateSliceDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error) {
	ref, err := normalizeSliceRef(ref)
	if err != nil {
		return nil, "", 0, nil, err
	}
	visibility = strings.TrimSpace(visibility)
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "public" {
		return nil, "", 0, nil, storage.ErrInvalid
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(includedPaths))
	for _, raw := range includedPaths {
		cleaned, err := canonicalIncludedPath(ref, raw)
		if err != nil {
			return nil, "", 0, nil, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	if len(out) == 0 {
		return nil, "", 0, nil, storage.ErrInvalid
	}
	if requiredApprovals < 0 {
		return nil, "", 0, nil, storage.ErrInvalid
	}
	checks := make([]string, 0, len(requiredChecks))
	checkSeen := map[string]struct{}{}
	for _, raw := range requiredChecks {
		check := strings.TrimSpace(raw)
		if check == "" || strings.Contains(check, ",") {
			return nil, "", 0, nil, storage.ErrInvalid
		}
		if _, ok := checkSeen[check]; ok {
			continue
		}
		checkSeen[check] = struct{}{}
		checks = append(checks, check)
	}
	return out, visibility, requiredApprovals, checks, nil
}

func canonicalIncludedPath(ref *corev1.SliceRef, raw string) (string, error) {
	cleaned, err := paths.Canonical(raw)
	if err != nil {
		value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if !strings.HasPrefix(value, "/") {
			value = "/" + value
		}
		value = path.Clean(value)
		if ref.Slice != "home" || strings.Count(strings.Trim(value, "/"), "/") != 0 {
			return "", storage.ErrInvalid
		}
		cleaned = value
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) == 0 || segments[0] != ref.Account {
		return "", storage.ErrInvalid
	}
	if len(segments) == 1 && ref.Slice != "home" {
		return "", storage.ErrInvalid
	}
	return cleaned, nil
}

func normalizeSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	if ref == nil {
		return nil, storage.ErrInvalid
	}
	account := normalizeSlug(ref.Account)
	slice := normalizeSlug(ref.Slice)
	if account == "" || slice == "" {
		return nil, storage.ErrInvalid
	}
	return &corev1.SliceRef{Account: account, Slice: slice}, nil
}

func normalizeSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" || len(value) > 63 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return ""
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return ""
	}
	return value
}

func normalizeSignupUsername(username string) (string, error) {
	return usernames.Normalize(username)
}

func sliceID(account, slice string) string { return "slice_" + account + "_" + slice }

func sliceRefKey(ref *corev1.SliceRef) string {
	if ref == nil {
		return ""
	}
	return normalizeSlug(ref.Account) + "/" + normalizeSlug(ref.Slice)
}

func sameSliceRef(a, b *corev1.SliceRef) bool { return sliceRefKey(a) == sliceRefKey(b) }
