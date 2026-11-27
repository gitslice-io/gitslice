package storage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// NormalizeSlug validates and canonicalizes an account or slice slug.
func NormalizeSlug(value, name string) (string, error) {
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

// NormalizeSliceRef validates and canonicalizes both slugs of a SliceRef.
func NormalizeSliceRef(ref *corev1.SliceRef) (*corev1.SliceRef, error) {
	if ref == nil {
		return nil, fmt.Errorf("%w: slice ref is required", ErrInvalid)
	}
	account, err := NormalizeSlug(ref.Account, "account")
	if err != nil {
		return nil, err
	}
	slug, err := NormalizeSlug(ref.Slice, "slice")
	if err != nil {
		return nil, err
	}
	return &corev1.SliceRef{Account: account, Slice: slug}, nil
}

// ValidateSliceDefinition normalizes and validates a slice definition against
// its owning slice ref. Both Postgres and memory stores delegate here so
// validation rules never diverge.
func ValidateSliceDefinition(ref *corev1.SliceRef, definition *corev1.SliceDefinition) (*corev1.SliceDefinition, error) {
	if definition == nil {
		return nil, fmt.Errorf("%w: slice definition is required", ErrInvalid)
	}
	ref, err := NormalizeSliceRef(ref)
	if err != nil {
		return nil, err
	}
	visibility := strings.TrimSpace(definition.Visibility)
	if visibility == "" {
		visibility = "private"
	}
	switch visibility {
	case "private", "public":
	default:
		return nil, fmt.Errorf("%w: visibility must be private or public", ErrInvalid)
	}
	if len(definition.IncludedPaths) == 0 {
		return nil, fmt.Errorf("%w: included path is required", ErrInvalid)
	}
	out := make([]string, 0, len(definition.IncludedPaths))
	seen := map[string]struct{}{}
	for _, raw := range definition.IncludedPaths {
		cleaned, err := canonicalSliceIncludedPath(ref, raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	normalizedApprovals, normalizedHumanApprovals, normalizedChecks, err := validateSubmitSettings(
		definition.RequiredApprovals,
		definition.RequiredHumanApprovals,
		definition.RequiredChecks,
	)
	if err != nil {
		return nil, err
	}
	approvers := strings.TrimSpace(definition.Approvers)
	if approvers != "" {
		acct, grp, err := ParseGroupRefStrict(approvers)
		if err != nil {
			return nil, err
		}
		approvers = FormatGroupHandle(acct, grp)
	}
	return &corev1.SliceDefinition{
		IncludedPaths:          out,
		Visibility:             visibility,
		RequiredApprovals:      normalizedApprovals,
		RequiredHumanApprovals: normalizedHumanApprovals,
		RequiredChecks:         normalizedChecks,
		Approvers:              approvers,
	}, nil
}

func validateSubmitSettings(requiredApprovals, requiredHumanApprovals int32, requiredChecks []string) (int32, int32, []string, error) {
	if requiredApprovals < 0 {
		return 0, 0, nil, fmt.Errorf("%w: required approvals must be zero or greater", ErrInvalid)
	}
	if requiredHumanApprovals < 0 {
		return 0, 0, nil, fmt.Errorf("%w: required human approvals must be zero or greater", ErrInvalid)
	}
	checks := make([]string, 0, len(requiredChecks))
	seen := map[string]struct{}{}
	for _, raw := range requiredChecks {
		check := strings.TrimSpace(raw)
		if check == "" {
			return 0, 0, nil, fmt.Errorf("%w: required check name is required", ErrInvalid)
		}
		if strings.Contains(check, ",") {
			return 0, 0, nil, fmt.Errorf("%w: required check %q must not contain commas; pass each check separately", ErrInvalid, check)
		}
		if _, ok := seen[check]; ok {
			continue
		}
		seen[check] = struct{}{}
		checks = append(checks, check)
	}
	return requiredApprovals, requiredHumanApprovals, checks, nil
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

// TokenHash returns the lowercase hex SHA-256 digest of a bearer/invite/login token.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// DefinitionHash computes the canonical content hash for a slice definition version.
func DefinitionHash(sliceID string, version int64, included []string, visibility string, requiredApprovals, requiredHumanApprovals int32, requiredChecks []string, approvers string) string {
	payload, _ := json.Marshal(struct {
		SliceID                string   `json:"slice_id"`
		Version                int64    `json:"version"`
		Included               []string `json:"included_paths"`
		Visibility             string   `json:"visibility"`
		RequiredApprovals      int32    `json:"required_approvals"`
		RequiredHumanApprovals int32    `json:"required_human_approvals,omitempty"`
		RequiredChecks         []string `json:"required_checks"`
		Approvers              string   `json:"approvers,omitempty"`
	}{
		SliceID:                sliceID,
		Version:                version,
		Included:               included,
		Visibility:             visibility,
		RequiredApprovals:      requiredApprovals,
		RequiredHumanApprovals: requiredHumanApprovals,
		RequiredChecks:         requiredChecks,
		Approvers:              strings.ToLower(strings.TrimSpace(approvers)),
	})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EncodeTimeIDCursor encodes a keyset cursor of the form RFC3339Nano|id using base64url.
func EncodeTimeIDCursor(ts time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts.UTC().Format(time.RFC3339Nano) + "|" + id))
}

// DecodeTimeIDCursor decodes a keyset cursor encoded by EncodeTimeIDCursor.
func DecodeTimeIDCursor(token string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid page token: %w", ErrInvalid)
	}
	stamp, id, ok := strings.Cut(string(raw), "|")
	ts, parseErr := time.Parse(time.RFC3339Nano, stamp)
	if !ok || id == "" || parseErr != nil {
		return time.Time{}, "", fmt.Errorf("invalid page token: %w", ErrInvalid)
	}
	return ts, id, nil
}

// SliceCursor represents a decoded keyset cursor for SliceStore.ListSorted.
type SliceCursor struct {
	IsHome bool
	Ts     time.Time
	Slug   string
}

// EncodeSliceCursor encodes a v1 keyset cursor for sorted slice listings.
func EncodeSliceCursor(isHome bool, ts time.Time, slug string) string {
	homeFlag := "0"
	if isHome {
		homeFlag = "1"
	}
	return fmt.Sprintf("v1|%s|%d|%s", homeFlag, ts.UTC().UnixNano(), slug)
}

// DecodeSliceCursor decodes a v1 keyset cursor for sorted slice listings.
func DecodeSliceCursor(raw string) (SliceCursor, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SliceCursor{}, false, nil
	}
	parts := strings.SplitN(raw, "|", 4)
	if len(parts) != 4 || parts[0] != "v1" || (parts[1] != "0" && parts[1] != "1") || parts[3] == "" {
		return SliceCursor{}, false, fmt.Errorf("%w: invalid slice cursor", ErrInvalid)
	}
	var nanos int64
	if _, err := fmt.Sscanf(parts[2], "%d", &nanos); err != nil {
		return SliceCursor{}, false, fmt.Errorf("%w: invalid slice cursor", ErrInvalid)
	}
	return SliceCursor{
		IsHome: parts[1] == "1",
		Ts:     time.Unix(0, nanos).UTC(),
		Slug:   parts[3],
	}, true, nil
}

// NormalizeCommitListLimit clamps commit-history page limits to (0, 500] with default 50.
func NormalizeCommitListLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 50
	}
	return limit
}

// CommitPageCursor represents a decoded commit-history pagination cursor.
type CommitPageCursor struct {
	CommitID    string `json:"commit_id"`
	CommittedAt string `json:"committed_at"`
}

// CommittedAt parses the cursor's RFC3339Nano timestamp.
func (c CommitPageCursor) CommittedAtTime() (time.Time, error) {
	committedAt, err := time.Parse(time.RFC3339Nano, c.CommittedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	return committedAt, nil
}

// CommitPageTokenForCommit encodes a commit-history cursor from a Commit proto.
func CommitPageTokenForCommit(commit *corev1.Commit) (string, error) {
	if commit == nil || commit.Id == "" {
		return "", fmt.Errorf("%w: commit page token cannot be built from empty commit", ErrInvalid)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, commit.CreatedAt)
	if err != nil {
		return "", fmt.Errorf("%w: invalid commit timestamp: %v", ErrInvalid, err)
	}
	return EncodeCommitPageToken(commit.Id, createdAt)
}

// EncodeCommitPageToken encodes a commit-history pagination cursor.
func EncodeCommitPageToken(commitID string, committedAt time.Time) (string, error) {
	if strings.TrimSpace(commitID) == "" || committedAt.IsZero() {
		return "", fmt.Errorf("%w: invalid commit page cursor", ErrInvalid)
	}
	raw, err := json.Marshal(CommitPageCursor{
		CommitID:    commitID,
		CommittedAt: committedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCommitPageToken decodes and validates a commit-history pagination cursor.
func DecodeCommitPageToken(token string) (*CommitPageCursor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	var cursor CommitPageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	if strings.TrimSpace(cursor.CommitID) == "" || strings.TrimSpace(cursor.CommittedAt) == "" {
		return nil, fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	if _, err := cursor.CommittedAtTime(); err != nil {
		return nil, err
	}
	return &cursor, nil
}
