package clibase

import (
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/workspace"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func ContainsFold(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

func NormalizeSlug(value, name string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" {
		return "", UserError("invalid_slug", name+" is required", "Use lower-case letters, numbers, '-' or '_'.")
	}
	if len(value) > 63 {
		return "", UserError("invalid_slug", name+" must be 63 characters or fewer", "Use a shorter "+name+".")
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", UserError("invalid_slug", name+" must not start or end with '-'", "Use lower-case letters, numbers, '-' or '_'.")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", UserError("invalid_slug", name+" may contain only letters, numbers, '-' or '_'", "Use lower-case letters, numbers, '-' or '_'.")
	}
	return value, nil
}

func ParseSliceRef(value string) (*corev1.SliceRef, error) {
	accountPart, slicePart, ok := model.SplitSliceHandle(value)
	if !ok {
		return nil, UserError("invalid_slice_ref", "slice must be account:slice", "Pass a slice reference such as acme:payment.")
	}
	account, err := NormalizeSlug(accountPart, "account")
	if err != nil {
		return nil, err
	}
	slug, err := NormalizeSlug(slicePart, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

// WorkspaceFolder resolves a canonical folder path (/acme/x) or a directory
// inside a gitslice workspace rooted relative to cwd.
func WorkspaceFolder(cwd, raw string) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "/") {
		folder, err := paths.CanonicalPrefix(strings.TrimSpace(raw))
		if err != nil {
			return "", UserError("invalid_argument", err.Error(), "")
		}
		return folder, nil
	}
	dir := cwd
	if raw != "" {
		dir = filepath.Join(dir, raw)
	}
	root, err := workspace.FindRoot(dir)
	if err != nil {
		return "", UserError("path_required", "not in a gitslice workspace", "Pass a canonical folder, such as /acme/services.")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", UserError("path_required", "cannot tell which folder that is", "Pass a canonical folder, such as /acme/services.")
	}
	folder, err := paths.CanonicalPrefix("/" + filepath.ToSlash(rel))
	if err != nil {
		return "", UserError("invalid_argument", err.Error(), "")
	}
	return folder, nil
}
