package storage

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/model"
)

const (
	MaxAPIKeyPurposeLength = 256
	MaxAPIKeyMetaIDLength  = 128
)

// NormalizeAPIKeyName trims a key's label and checks it. An empty label
// becomes "key".
func NormalizeAPIKeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "key", nil
	}
	if utf8.RuneCountInString(name) > MaxAPIKeyNameLength {
		return "", fmt.Errorf("%w: key name must be %d characters or fewer", ErrInvalid, MaxAPIKeyNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: key name must not contain control characters", ErrInvalid)
		}
	}
	return name, nil
}

// NormalizeAPIKeyMaxRole validates an optional API key max_role ("", "reader",
// "contributor", or "writer").
func NormalizeAPIKeyMaxRole(role string) (string, error) {
	return model.NormalizeAPIKeyMaxRole(role)
}

// APIKeyRoleTier orders API key max_role values. Empty (uncapped) returns 4 so
// attenuation comparisons treat uncapped > writer > contributor > reader.
func APIKeyRoleTier(role string) int {
	return model.APIKeyRoleTier(role)
}

// NormalizeAPIKeyText validates optional key audit fields (purpose, daemon_id,
// conversation_id).
func NormalizeAPIKeyText(field, value string, maxLen int) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxLen {
		return "", fmt.Errorf("%w: %s must be %d characters or fewer", ErrInvalid, field, maxLen)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: %s must not contain control characters", ErrInvalid, field)
		}
	}
	return value, nil
}

// DeduplicateSliceIDs trims empty entries and preserves first-seen order.
func DeduplicateSliceIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// NormalizeCreateAPIKeyInput validates and normalizes input fields before
// parent-key attenuation is applied.
func NormalizeCreateAPIKeyInput(in CreateAPIKeyInput, now time.Time) (CreateAPIKeyInput, error) {
	var err error
	in.ActorID = strings.TrimSpace(in.ActorID)
	in.AgentAccount = strings.TrimSpace(in.AgentAccount)
	in.ParentKeyID = strings.TrimSpace(in.ParentKeyID)
	if in.Name, err = NormalizeAPIKeyName(in.Name); err != nil {
		return CreateAPIKeyInput{}, err
	}
	if in.MaxRole, err = NormalizeAPIKeyMaxRole(in.MaxRole); err != nil {
		return CreateAPIKeyInput{}, err
	}
	if in.Purpose, err = NormalizeAPIKeyText("purpose", in.Purpose, MaxAPIKeyPurposeLength); err != nil {
		return CreateAPIKeyInput{}, err
	}
	if in.DaemonID, err = NormalizeAPIKeyText("daemon_id", in.DaemonID, MaxAPIKeyMetaIDLength); err != nil {
		return CreateAPIKeyInput{}, err
	}
	if in.ConversationID, err = NormalizeAPIKeyText("conversation_id", in.ConversationID, MaxAPIKeyMetaIDLength); err != nil {
		return CreateAPIKeyInput{}, err
	}
	if in.HasSliceScope {
		in.SliceIDs = DeduplicateSliceIDs(in.SliceIDs)
		if in.SliceIDs == nil {
			in.SliceIDs = []string{}
		}
	} else {
		in.SliceIDs = nil
	}
	if !in.ExpiresAt.IsZero() {
		in.ExpiresAt = in.ExpiresAt.UTC()
		if !in.ExpiresAt.After(now) {
			return CreateAPIKeyInput{}, fmt.Errorf("%w: key expiry must be in the future", ErrInvalid)
		}
	}
	return in, nil
}

// AttenuateAPIKeyInput ensures a child API key never exceeds its parent's
// slice scope, max_role, expiry, or bound daemon/conversation metadata, and
// inherits parent ceilings when omitted by the child request.
func AttenuateAPIKeyInput(parent APIKey, in CreateAPIKeyInput, now time.Time) (CreateAPIKeyInput, error) {
	if !parent.Active(now) {
		return CreateAPIKeyInput{}, fmt.Errorf("%w: parent API key is revoked or expired", ErrUnauthenticated)
	}
	if parent.HasSliceScope {
		if !in.HasSliceScope {
			in.HasSliceScope = true
			in.SliceIDs = slices.Clone(parent.SliceIDs)
			if in.SliceIDs == nil {
				in.SliceIDs = []string{}
			}
		} else {
			for _, id := range in.SliceIDs {
				if !slices.Contains(parent.SliceIDs, id) {
					return CreateAPIKeyInput{}, fmt.Errorf("%w: requested slice %s exceeds parent key scope", ErrUnauthorized, id)
				}
			}
		}
	}
	if parent.MaxRole != "" {
		if in.MaxRole == "" {
			in.MaxRole = parent.MaxRole
		} else if APIKeyRoleTier(in.MaxRole) > APIKeyRoleTier(parent.MaxRole) {
			return CreateAPIKeyInput{}, fmt.Errorf("%w: requested max_role %q exceeds parent key role %q", ErrUnauthorized, in.MaxRole, parent.MaxRole)
		}
	}
	if !parent.ExpiresAt.IsZero() {
		if in.ExpiresAt.IsZero() {
			in.ExpiresAt = parent.ExpiresAt.UTC()
		} else if in.ExpiresAt.After(parent.ExpiresAt) {
			return CreateAPIKeyInput{}, fmt.Errorf("%w: requested expiry exceeds parent key expiry", ErrUnauthorized)
		}
	}
	if parent.DaemonID != "" {
		if in.DaemonID == "" {
			in.DaemonID = parent.DaemonID
		} else if in.DaemonID != parent.DaemonID {
			return CreateAPIKeyInput{}, fmt.Errorf("%w: requested daemon_id differs from parent key", ErrUnauthorized)
		}
	}
	if parent.ConversationID != "" {
		if in.ConversationID == "" {
			in.ConversationID = parent.ConversationID
		} else if in.ConversationID != parent.ConversationID {
			return CreateAPIKeyInput{}, fmt.Errorf("%w: requested conversation_id differs from parent key", ErrUnauthorized)
		}
	}
	return in, nil
}

// FilterAPIKeysForCallerKey restricts a subject's key listing when the caller
// authenticated via an attenuated or child API key: root unscoped keys see all
// of the subject's keys, while scoped/capped/child keys see only themselves and
// their transitive descendants.
func FilterAPIKeysForCallerKey(keys []APIKey, callerKeyID string) []APIKey {
	byID := make(map[string]APIKey, len(keys))
	for _, k := range keys {
		byID[k.ID] = k
	}
	callerKey, ok := byID[callerKeyID]
	if !ok {
		return nil
	}
	if !callerKey.HasSliceScope && callerKey.MaxRole == "" && callerKey.ParentKeyID == "" {
		return keys
	}
	allowed := map[string]bool{callerKeyID: true}
	changed := true
	for changed {
		changed = false
		for _, k := range keys {
			if !allowed[k.ID] && k.ParentKeyID != "" && allowed[k.ParentKeyID] {
				allowed[k.ID] = true
				changed = true
			}
		}
	}
	out := make([]APIKey, 0, len(allowed))
	for _, k := range keys {
		if allowed[k.ID] {
			out = append(out, k)
		}
	}
	return out
}

// NormalizeDelegatedAgentDisplayName trims displayName, falls back to
// fallback, and checks length and control characters.
func NormalizeDelegatedAgentDisplayName(displayName, fallback string) (string, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = fallback
	}
	if utf8.RuneCountInString(displayName) > MaxAPIKeyNameLength {
		return "", fmt.Errorf("%w: display_name must be %d characters or fewer", ErrInvalid, MaxAPIKeyNameLength)
	}
	for _, r := range displayName {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: display_name must not contain control characters", ErrInvalid)
		}
	}
	return displayName, nil
}

// NormalizeDelegatedAgentSliceRole validates the optional slice role for
// CreateDelegatedAgent. Empty sliceID forbids a non-empty role; non-empty
// sliceID defaults an empty role to "contributor".
func NormalizeDelegatedAgentSliceRole(sliceID, role string) (string, error) {
	return model.NormalizeDelegatedAgentSliceRole(sliceID, role)
}

// NormalizeConversationRole validates a conversation role, defaulting empty to
// "contributor".
func NormalizeConversationRole(raw string) (string, error) {
	role, err := NormalizeAPIKeyMaxRole(raw)
	if err != nil {
		return "", fmt.Errorf("%w: conversation role must be reader, contributor, or writer", ErrInvalid)
	}
	if role == "" {
		return "contributor", nil
	}
	return role, nil
}
