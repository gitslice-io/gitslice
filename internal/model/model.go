package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	DefaultTargetRef           = "refs/global/main"
	ShortChangesetIDLen        = 10
	APIKeyPrefix               = "gsk_"
	MinRegistrationTokenLength = 32
	MaxAPIKeyNameLength        = 64
	MaxSecretValueBytes        = 48 << 10
	CheckStatusPass            = "pass"
	CheckStatusFail            = "fail"
	SubmitMergeKind            = "submit_merge"

	maxEmailBytes = 254
)

var ErrInvalid = errors.New("invalid")

// AccountRoles lists membership roles from most to least privileged.
var AccountRoles = []string{"owner", "admin", "writer", "member", "contributor", "reader"}

// AccountRoleRank orders roles (lower is more privileged); unknown roles rank
// after every known one.
func AccountRoleRank(role string) int {
	for i, known := range AccountRoles {
		if role == known {
			return i
		}
	}
	return len(AccountRoles)
}

// SliceRoles are the roles a slice member can have (design/26_slice_members.md,
// design/36_multi_agent_collaboration.md).
var SliceRoles = []string{"admin", "writer", "contributor", "reader"}

// SliceRoleRank orders slice roles (lower is more privileged); unknown roles
// rank after every known one.
func SliceRoleRank(role string) int {
	for i, known := range SliceRoles {
		if role == known {
			return i
		}
	}
	return len(SliceRoles)
}

// ShortChangesetID returns the canonical short, shareable code for a changeset
// id: the hex portion after the "cs_" prefix, truncated to ShortChangesetIDLen.
// Returns "" for an empty id. If the id has no "cs_" prefix it is treated as the
// hex body as-is.
func ShortChangesetID(id string) string {
	if id == "" {
		return ""
	}
	body := strings.TrimPrefix(id, "cs_")
	if len(body) > ShortChangesetIDLen {
		return body[:ShortChangesetIDLen]
	}
	return body
}

// ChangesetIDLookupPrefix validates a user-supplied changeset id or short code
// and returns the canonical lookup prefix ("cs_" + lowercased hex) for a
// left-anchored prefix match. ok is false for handles, empty, non-hex, or hex
// shorter than 4 / longer than 32 chars. It accepts an optional leading "cs_".
func ChangesetIDLookupPrefix(selector string) (prefix string, ok bool) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", false
	}
	body := strings.TrimPrefix(strings.ToLower(selector), "cs_")
	if len(body) < 4 || len(body) > 32 {
		return "", false
	}
	for _, ch := range body {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return "", false
		}
	}
	return "cs_" + body, true
}

// CommitIDLookupPrefix reads a selector as a commit id or a prefix of one
// ("sha256:" optional, at least 7 hex chars) and returns the "sha256:"-prefixed
// form for a left-anchored match.
func CommitIDLookupPrefix(selector string) (prefix string, ok bool) {
	body := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(selector)), "sha256:")
	if len(body) < 7 || len(body) > 64 {
		return "", false
	}
	for _, ch := range body {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return "", false
		}
	}
	return "sha256:" + body, true
}

// SliceHandle is the canonical global slice identity, account:slice.
func SliceHandle(ref *corev1.SliceRef) string {
	if ref == nil || ref.Account == "" || ref.Slice == "" {
		return ""
	}
	return ref.Account + ":" + ref.Slice
}

// SplitSliceHandle parses an "account:slice" handle, also accepting the legacy
// "account/slice" form for backward compatibility.
func SplitSliceHandle(ref string) (account, slice string, ok bool) {
	ref = strings.TrimSpace(ref)
	sep := "/"
	if strings.Contains(ref, ":") {
		sep = ":"
	}
	parts := strings.SplitN(ref, sep, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// NormalizeAPIKeyMaxRole validates an optional API key max_role ("", "reader",
// "contributor", or "writer").
func NormalizeAPIKeyMaxRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "", "reader", "contributor", "writer":
		return role, nil
	default:
		return "", fmt.Errorf("%w: max_role must be reader, contributor, or writer", ErrInvalid)
	}
}

// APIKeyRoleTier orders API key max_role values. Empty (uncapped) returns 4 so
// attenuation comparisons treat uncapped > writer > contributor > reader.
func APIKeyRoleTier(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "reader":
		return 1
	case "contributor":
		return 2
	case "writer":
		return 3
	case "":
		return 4
	default:
		return 0
	}
}

// NormalizeDelegatedAgentSliceRole validates the optional slice role for
// CreateDelegatedAgent. Empty sliceID forbids a non-empty role; non-empty
// sliceID defaults an empty role to "contributor".
func NormalizeDelegatedAgentSliceRole(sliceID, role string) (string, error) {
	sliceID = strings.TrimSpace(sliceID)
	role = strings.ToLower(strings.TrimSpace(role))
	if sliceID == "" {
		if role != "" {
			return "", fmt.Errorf("%w: role requires a slice", ErrInvalid)
		}
		return "", nil
	}
	if role == "" {
		return "contributor", nil
	}
	if !slices.Contains(SliceRoles, role) {
		return "", fmt.Errorf("%w: role must be one of %s", ErrInvalid, strings.Join(SliceRoles, ", "))
	}
	return role, nil
}

// NormalizeOwnerEmail trims and lowercases an agent owner email and applies a
// loose shape check.
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

// ValidSliceSecretName reports whether name matches ^[A-Z_][A-Z0-9_]*$.
func ValidSliceSecretName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if i == 0 {
			if c == '_' || (c >= 'A' && c <= 'Z') {
				continue
			}
			return false
		}
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// NormalizeCheckStatus canonicalizes a check result status ("pass" or "fail").
func NormalizeCheckStatus(status string) (string, bool) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case CheckStatusPass, CheckStatusFail:
		return status, true
	default:
		return "", false
	}
}

// NormalizeCheckRunStatus validates and canonicalizes a CheckRun lifecycle
// status ("queued", "running", "passed", "failed", "errored", "skipped", or
// "canceled"). Empty status defaults to "queued".
func NormalizeCheckRunStatus(status string) (string, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return "queued", nil
	}
	switch status {
	case "queued", "running", "passed", "failed", "errored", "skipped", "canceled":
		return status, nil
	default:
		return "", fmt.Errorf("%w: unsupported check run status %q", ErrInvalid, status)
	}
}

// IsTerminalCheckRunStatus reports whether a CheckRun has reached a final state.
func IsTerminalCheckRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "passed", "failed", "errored", "skipped", "canceled", "cancelled":
		return true
	default:
		return false
	}
}

// TerminalCheckResultStatus maps a terminal CheckRun status to its binary
// submit-requirement check result ("pass" or "fail"), or ok=false if the status
// does not produce a pass/fail verdict (e.g. skipped or canceled).
func TerminalCheckResultStatus(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "passed":
		return NormalizeCheckStatus(CheckStatusPass)
	case "failed", "errored":
		return NormalizeCheckStatus(CheckStatusFail)
	default:
		return "", false
	}
}

// FormatActorAttribution renders an ActorAttribution in the canonical human
// format: "<acting> (for <controller>) via <daemon_name || daemon_id>, conversation <conv_id>".
func FormatActorAttribution(attr *corev1.ActorAttribution, fallbackUsername string) string {
	acting := strings.TrimSpace(attr.GetActingUsername())
	if acting == "" {
		acting = strings.TrimSpace(fallbackUsername)
	}
	if acting == "" {
		acting = strings.TrimSpace(attr.GetActingSubjectId())
	}
	if acting == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString(acting)

	ctrl := strings.TrimSpace(attr.GetControllerUsername())
	if ctrl == "" && strings.TrimSpace(attr.GetControllerSubjectId()) != "" && strings.TrimSpace(attr.GetControllerSubjectId()) != strings.TrimSpace(attr.GetActingSubjectId()) {
		ctrl = strings.TrimSpace(attr.GetControllerSubjectId())
	}
	if ctrl != "" && ctrl != acting && (strings.TrimSpace(attr.GetControllerSubjectId()) == "" || strings.TrimSpace(attr.GetControllerSubjectId()) != strings.TrimSpace(attr.GetActingSubjectId())) {
		fmt.Fprintf(&b, " (for %s)", ctrl)
	}

	daemon := strings.TrimSpace(attr.GetDaemonName())
	if daemon == "" {
		daemon = strings.TrimSpace(attr.GetDaemonId())
	}
	if daemon != "" {
		fmt.Fprintf(&b, " via %s", daemon)
	}

	if conv := strings.TrimSpace(attr.GetConversationId()); conv != "" {
		fmt.Fprintf(&b, ", conversation %s", conv)
	}
	return b.String()
}
