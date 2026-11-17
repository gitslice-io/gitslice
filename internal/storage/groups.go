package storage

import (
	"fmt"
	"strings"
	"time"
)

// AccountGroup is an account-level named list of user and agent subjects
// referenced as @account/name (design/36_multi_agent_collaboration.md).
type AccountGroup struct {
	ID          string
	Account     string
	Name        string
	Handle      string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Members     []AccountGroupMember
	MemberCount int32
}

// AccountGroupMember is one subject belonging to an AccountGroup.
type AccountGroupMember struct {
	SubjectID   string
	Username    string
	DisplayName string
	Kind        string
	AddedBy     string
	CreatedAt   time.Time
}

// SliceGroupMember is an account group given a role on one slice.
type SliceGroupMember struct {
	SliceID     string
	GroupID     string
	Account     string
	Name        string
	Handle      string
	Role        string
	AddedBy     string
	CreatedAt   time.Time
	MemberCount int32
}

// NormalizeGroupName validates and canonicalizes a group or account slug
// component used in @account/name references.
func NormalizeGroupName(name string) (string, error) {
	return normalizeGroupSlug(name, "group name")
}

// NormalizeGroupAccount validates and canonicalizes the account slug part of
// @account/name.
func NormalizeGroupAccount(account string) (string, error) {
	return normalizeGroupSlug(account, "account")
}

func normalizeGroupSlug(value, label string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalid, label)
	}
	if len(value) > 63 {
		return "", fmt.Errorf("%w: %s must be 63 characters or fewer", ErrInvalid, label)
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", fmt.Errorf("%w: %s must not start or end with '-'", ErrInvalid, label)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("%w: %s may contain only letters, numbers, '-' or '_'", ErrInvalid, label)
	}
	return value, nil
}

// FormatGroupHandle formats a canonical "@account/name" group handle.
func FormatGroupHandle(account, name string) string {
	account = strings.ToLower(strings.TrimSpace(account))
	name = strings.ToLower(strings.TrimSpace(name))
	if account == "" || name == "" {
		return ""
	}
	return "@" + account + "/" + name
}

// ParseGroupRef parses a canonical group reference "@account/name".
func ParseGroupRef(ref string) (account, name string, ok bool) {
	a, n, err := ParseGroupRefStrict(ref)
	if err != nil {
		return "", "", false
	}
	return a, n, true
}

// ParseGroupRefStrict parses "@account/name" and returns a descriptive
// ErrInvalid error when malformed.
func ParseGroupRefStrict(ref string) (string, string, error) {
	trimmed := strings.TrimSpace(ref)
	if !strings.HasPrefix(trimmed, "@") {
		return "", "", fmt.Errorf("%w: group reference %q must have the form @account/name", ErrInvalid, ref)
	}
	rest := strings.TrimPrefix(trimmed, "@")
	rawAccount, rawName, found := strings.Cut(rest, "/")
	if !found || strings.Contains(rawName, "/") {
		return "", "", fmt.Errorf("%w: group reference %q must have the form @account/name", ErrInvalid, ref)
	}
	account, err := NormalizeGroupAccount(rawAccount)
	if err != nil {
		return "", "", err
	}
	name, err := NormalizeGroupName(rawName)
	if err != nil {
		return "", "", err
	}
	return account, name, nil
}
