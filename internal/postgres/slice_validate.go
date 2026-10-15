package postgres

import (
	"fmt"
	"path"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func normalizeSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	if ref == nil {
		return nil, fmt.Errorf("%w: slice ref is required", ErrInvalid)
	}
	account, err := normalizeSlug(ref.Account, "account")
	if err != nil {
		return nil, err
	}
	slug, err := normalizeSlug(ref.Slice, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

func normalizeSlug(value, name string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	if len(value) > 63 {
		return "", fmt.Errorf("%w: %s must be 63 characters or fewer", ErrInvalid, name)
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", fmt.Errorf("%w: %s must not start or end with '-'", ErrInvalid, name)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("%w: %s may contain only letters, numbers, '-' or '_'", ErrInvalid, name)
	}
	return value, nil
}

func validateSliceDefinition(ref *corev1.SliceRef, includedPaths []string, visibility string, requiredApprovals int32, requiredChecks []string) ([]string, string, int32, []string, error) {
	if ref == nil {
		return nil, "", 0, nil, fmt.Errorf("%w: slice ref is required", ErrInvalid)
	}
	visibility = strings.TrimSpace(visibility)
	if visibility == "" {
		visibility = "private"
	}
	switch visibility {
	case "private", "public":
	default:
		return nil, "", 0, nil, fmt.Errorf("%w: visibility must be private or public", ErrInvalid)
	}
	if len(includedPaths) == 0 {
		return nil, "", 0, nil, fmt.Errorf("%w: included path is required", ErrInvalid)
	}
	out := make([]string, 0, len(includedPaths))
	seen := map[string]struct{}{}
	for _, raw := range includedPaths {
		cleaned, err := canonicalSliceIncludedPath(ref, raw)
		if err != nil {
			return nil, "", 0, nil, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	normalizedApprovals, normalizedChecks, err := validateSubmitSettings(requiredApprovals, requiredChecks)
	if err != nil {
		return nil, "", 0, nil, err
	}
	return out, visibility, normalizedApprovals, normalizedChecks, nil
}

func validateSubmitSettings(requiredApprovals int32, requiredChecks []string) (int32, []string, error) {
	if requiredApprovals < 0 {
		return 0, nil, fmt.Errorf("%w: required approvals must be zero or greater", ErrInvalid)
	}
	checks := make([]string, 0, len(requiredChecks))
	seen := map[string]struct{}{}
	for _, raw := range requiredChecks {
		check := strings.TrimSpace(raw)
		if check == "" {
			return 0, nil, fmt.Errorf("%w: required check name is required", ErrInvalid)
		}
		if strings.Contains(check, ",") {
			return 0, nil, fmt.Errorf("%w: required check %q must not contain commas; pass each check separately", ErrInvalid, check)
		}
		if _, ok := seen[check]; ok {
			continue
		}
		seen[check] = struct{}{}
		checks = append(checks, check)
	}
	return requiredApprovals, checks, nil
}

func canonicalSliceIncludedPath(ref *corev1.SliceRef, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: included path is required", ErrInvalid)
	}
	if strings.Contains(value, ",") {
		return "", fmt.Errorf("%w: included path %q must not contain commas; pass each included path separately", ErrInvalid, value)
	}
	cleaned, err := paths.Canonical(value)
	if err != nil {
		accountRoot, rootErr := canonicalAccountRootPath(value)
		if rootErr != nil || ref.Slice != "home" {
			return "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		cleaned = accountRoot
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) == 0 || segments[0] != ref.Account {
		return "", fmt.Errorf("%w: included path %s must be under /%s", ErrInvalid, cleaned, ref.Account)
	}
	if len(segments) == 1 && ref.Slice != "home" {
		return "", fmt.Errorf("%w: only home slices may include account root %s", ErrInvalid, cleaned)
	}
	return cleaned, nil
}

func canonicalAccountRootPath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("path must include account segment")
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) != 1 {
		return "", fmt.Errorf("path is not an account root")
	}
	if segments[0] == "" || segments[0] == "." || segments[0] == ".." {
		return "", fmt.Errorf("invalid account path segment")
	}
	return cleaned, nil
}
