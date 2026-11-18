// Package intents implements pattern validation, duration parsing, and
// overlap matching for advisory path intents (§6).
package intents

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/paths"
)

const (
	// DefaultTTL is used when a caller does not specify a duration for an intent.
	DefaultTTL = 2 * time.Hour

	// MaxTTL caps how long a single advisory path intent can remain active.
	MaxTTL = 7 * 24 * time.Hour

	// MaxMessageLength caps advisory intent reason text.
	MaxMessageLength = 2000

	// MaxActivePerSubject caps one subject's active intents in one slice, and
	// MaxActivePerSlice caps a slice's active intents, so listing them and
	// matching them against every changeset update stays cheap.
	MaxActivePerSubject = 20
	MaxActivePerSlice   = 200
)

var intentIDRegex = regexp.MustCompile(`\bint_[0-9a-zA-Z]+\b`)

// ActiveIntent describes an unexpired advisory path intent for overlap checks.
type ActiveIntent struct {
	ID               string
	SliceID          string
	Pattern          string
	Message          string
	CreatorSubjectID string
	CreatorName      string
	ChangesetID      string
	ExpiresAt        time.Time
}

// OpenChangeset describes an active changeset and its affected paths for
// overlap warnings.
type OpenChangeset struct {
	ID            string
	Title         string
	Author        string
	AffectedPaths []string
}

// OverlapOptions configures filtering when computing advisory overlap warnings.
type OverlapOptions struct {
	ExcludeSubjectID    string
	ExcludeChangesetID  string
	ExcludeChangesetIDs []string
	Now                 time.Time
}

func (o OverlapOptions) excludesChangeset(id string) bool {
	if id == "" {
		return false
	}
	if o.ExcludeChangesetID != "" && o.ExcludeChangesetID == id {
		return true
	}
	return slices.Contains(o.ExcludeChangesetIDs, id)
}

// OverlapWarning describes a single advisory conflict with an active intent or
// another open changeset.
type OverlapWarning struct {
	Kind            string // "intent" or "changeset"
	ID              string
	Owner           string
	Pattern         string
	Message         string
	ExpiresAt       time.Time
	MatchedPaths    []string
	ConflictPattern string
}

// NormalizePattern validates and canonicalizes an intent path or glob pattern.
// Patterns must be UTF-8 canonical slash-separated paths starting with '/',
// without Windows backslashes, '.' or '..' traversal segments, or leading '!'
// negation. A trailing '/' is normalized to '/**'.
func NormalizePattern(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("intent pattern cannot be empty")
	}
	if !utf8.ValidString(trimmed) {
		return "", errors.New("intent pattern must be valid UTF-8")
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", errors.New("intent pattern cannot contain control characters")
		}
	}
	if strings.Contains(trimmed, `\`) {
		return "", errors.New("intent pattern cannot contain backslashes")
	}
	if strings.HasPrefix(trimmed, "!") {
		return "", errors.New("intent pattern negation ('!') is not supported")
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("intent pattern %q must start with '/'", trimmed)
	}

	hasTrailingSlash := strings.HasSuffix(trimmed, "/")
	for strings.Contains(trimmed, "//") {
		trimmed = strings.ReplaceAll(trimmed, "//", "/")
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return "", errors.New("intent pattern cannot target root '/'")
	}

	segments := strings.Split(trimmed[1:], "/")
	for _, seg := range segments {
		if seg == "" {
			return "", fmt.Errorf("intent pattern %q contains empty path segment", raw)
		}
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("intent pattern %q cannot contain '.' or '..' traversal segments", raw)
		}
	}

	if hasTrailingSlash && segments[len(segments)-1] != "**" {
		trimmed += "/**"
	}
	if !doublestar.ValidatePattern(trimmed) {
		return "", fmt.Errorf("invalid glob syntax in intent pattern %q", raw)
	}
	return trimmed, nil
}

// StaticPrefix returns the longest non-glob canonical path prefix before the
// first segment containing glob metacharacters ('*', '?', '[', '{').
// Returns "" if the very first segment contains a glob metacharacter.
func StaticPrefix(pattern string) string {
	trimmed := strings.TrimPrefix(pattern, "/")
	if trimmed == "" {
		return ""
	}
	segments := strings.Split(trimmed, "/")
	static := make([]string, 0, len(segments))
	for _, seg := range segments {
		if segmentHasGlob(seg) {
			break
		}
		static = append(static, seg)
	}
	if len(static) == 0 {
		return ""
	}
	return "/" + strings.Join(static, "/")
}

// ValidateSlicePattern validates and normalizes an intent pattern against a
// slice's included path prefixes so an intent cannot declare coverage outside
// the slice.
func ValidateSlicePattern(raw string, includedPaths []string) (string, error) {
	normalized, err := NormalizePattern(raw)
	if err != nil {
		return "", err
	}
	staticPrefix := StaticPrefix(normalized)
	if staticPrefix == "" {
		return "", fmt.Errorf("intent pattern %q must have a non-glob path prefix within the slice", raw)
	}
	if len(includedPaths) == 0 {
		if _, err := paths.Canonical(staticPrefix); err != nil {
			return "", fmt.Errorf("intent pattern %q prefix %q is outside slice bounds: %w", raw, staticPrefix, err)
		}
		return normalized, nil
	}

	canonicalPrefixes := make([]string, 0, len(includedPaths))
	for _, inc := range includedPaths {
		cp, err := paths.CanonicalPrefix(inc)
		if err != nil {
			continue
		}
		canonicalPrefixes = append(canonicalPrefixes, cp)
	}
	if len(canonicalPrefixes) == 0 || !paths.InAnyPrefix(canonicalPrefixes, staticPrefix) {
		return "", fmt.Errorf("intent pattern %q prefix %q is outside slice included paths", raw, staticPrefix)
	}
	return normalized, nil
}

// ParseDuration parses a positive intent TTL duration. Supports standard Go
// durations (such as "30m", "2h", "1h30m") as well as "<N>d" day suffixes
// (e.g., "1d", "7d", or "1d12h"). Enforces 0 < d <= MaxTTL.
func ParseDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("duration cannot be empty")
	}

	var total time.Duration
	if idx := strings.IndexByte(s, 'd'); idx >= 0 {
		dayPart := s[:idx]
		rest := s[idx+1:]
		days, err := strconv.Atoi(dayPart)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid duration %q", raw)
		}
		dayDur := time.Duration(days) * 24 * time.Hour
		if dayDur/24/time.Hour != time.Duration(days) {
			return 0, fmt.Errorf("duration %q overflows", raw)
		}
		total = dayDur
		if rest != "" {
			extra, err := time.ParseDuration(rest)
			if err != nil || extra < 0 {
				return 0, fmt.Errorf("invalid duration %q", raw)
			}
			total += extra
		}
	} else {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		total = d
	}

	if total <= 0 {
		return 0, fmt.Errorf("intent duration %q must be positive", raw)
	}
	if total > MaxTTL {
		return 0, fmt.Errorf("intent duration %q exceeds maximum 7d", raw)
	}
	return total, nil
}

// FormatRemaining formats a remaining TTL concisely for terminal display.
func FormatRemaining(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	if d < time.Minute {
		secs := int(d.Round(time.Second).Seconds())
		if secs < 1 {
			secs = 1
		}
		return fmt.Sprintf("%ds", secs)
	}
	totalMinutes := int(d.Round(time.Minute).Minutes())
	days := totalMinutes / (24 * 60)
	hours := (totalMinutes % (24 * 60)) / 60
	mins := totalMinutes % 60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// ExtractMentionedIntentIDs returns unique intent IDs ("int_...") referenced in
// changeset titles or descriptions, preserving first-appearance order.
func ExtractMentionedIntentIDs(texts ...string) []string {
	var ids []string
	seen := make(map[string]struct{})
	for _, t := range texts {
		for _, match := range intentIDRegex.FindAllString(t, -1) {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			ids = append(ids, match)
		}
	}
	return ids
}

func hasGlob(pattern string) bool {
	return segmentHasGlob(pattern)
}

func segmentHasGlob(seg string) bool {
	return strings.ContainsAny(seg, "*?[{")
}
