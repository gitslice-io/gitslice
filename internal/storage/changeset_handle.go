package storage

import (
	"strings"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

const ShortChangesetIDLen = 10

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

// SliceHandle is the canonical global slice identity, account:slice. The colon
// keeps it visually distinct from a "/account/slice" repository path.
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

func PopulateChangesetHandles(cs *corev1.Changeset) {
	if cs == nil {
		return
	}
}
