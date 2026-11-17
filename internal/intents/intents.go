// Package intents implements pattern validation, duration parsing, and
// overlap matching for advisory path intents (§6).
package intents

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
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

// MatchesPath reports whether a canonical filePath falls under pattern.
// Non-glob patterns match either exact file equality or directory prefix
// containment via paths.Contains; glob patterns use doublestar matching.
func MatchesPath(pattern, filePath string) bool {
	if pattern == "" || filePath == "" {
		return false
	}
	if !hasGlob(pattern) {
		return paths.Contains(pattern, filePath)
	}
	matched, err := doublestar.Match(pattern, filePath)
	return err == nil && matched
}

// OverlappingPaths returns the sorted, deduplicated subset of candidatePaths
// matched by pattern.
func OverlappingPaths(pattern string, candidatePaths []string) []string {
	if pattern == "" || len(candidatePaths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(candidatePaths))
	var out []string
	for _, p := range candidatePaths {
		if _, dup := seen[p]; dup {
			continue
		}
		if MatchesPath(pattern, p) {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// PatternsOverlap reports whether two canonical intent patterns have a
// non-empty intersection of potentially matched file paths.
func PatternsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if !hasGlob(a) && !hasGlob(b) {
		return paths.Contains(a, b) || paths.Contains(b, a)
	}
	pa := StaticPrefix(a)
	pb := StaticPrefix(b)
	if pa != "" && pb != "" && !paths.Contains(pa, pb) && !paths.Contains(pb, pa) {
		return false
	}

	variantsA := patternVariants(a)
	variantsB := patternVariants(b)
	for _, va := range variantsA {
		for _, vb := range variantsB {
			if segListsOverlap(va, vb) {
				return true
			}
		}
	}
	return false
}

// FindPathOverlaps returns advisory warnings for targetPaths against active
// intents and open changesets.
func FindPathOverlaps(targetPaths []string, active []ActiveIntent, openChangesets []OpenChangeset, opts OverlapOptions) []OverlapWarning {
	if len(targetPaths) == 0 {
		return nil
	}
	cleanTargets := dedupSorted(targetPaths)
	var warnings []OverlapWarning

	for _, intent := range active {
		if !opts.Now.IsZero() && !intent.ExpiresAt.IsZero() && !intent.ExpiresAt.After(opts.Now) {
			continue
		}
		if opts.ExcludeSubjectID != "" && intent.CreatorSubjectID == opts.ExcludeSubjectID {
			continue
		}
		if opts.excludesChangeset(intent.ChangesetID) {
			continue
		}
		matched := OverlappingPaths(intent.Pattern, cleanTargets)
		if len(matched) == 0 {
			continue
		}
		warnings = append(warnings, OverlapWarning{
			Kind:         "intent",
			ID:           intent.ID,
			Owner:        displayOwner(intent.CreatorName, intent.CreatorSubjectID),
			Pattern:      intent.Pattern,
			Message:      intent.Message,
			ExpiresAt:    intent.ExpiresAt,
			MatchedPaths: matched,
		})
	}

	targetSet := make(map[string]struct{}, len(cleanTargets))
	for _, p := range cleanTargets {
		targetSet[p] = struct{}{}
	}
	for _, cs := range openChangesets {
		if opts.excludesChangeset(cs.ID) {
			continue
		}
		matched := intersectSets(targetSet, cs.AffectedPaths)
		if len(matched) == 0 {
			continue
		}
		warnings = append(warnings, OverlapWarning{
			Kind:         "changeset",
			ID:           cs.ID,
			Owner:        cs.Author,
			Message:      cs.Title,
			MatchedPaths: matched,
		})
	}
	return warnings
}

// FindPatternOverlaps checks whether candidatePattern overlaps any active
// intents or open changesets (used when declaring a new intent).
func FindPatternOverlaps(candidatePattern string, active []ActiveIntent, openChangesets []OpenChangeset, opts OverlapOptions) ([]OverlapWarning, error) {
	norm, err := NormalizePattern(candidatePattern)
	if err != nil {
		return nil, err
	}
	var warnings []OverlapWarning

	for _, intent := range active {
		if !opts.Now.IsZero() && !intent.ExpiresAt.IsZero() && !intent.ExpiresAt.After(opts.Now) {
			continue
		}
		if opts.ExcludeSubjectID != "" && intent.CreatorSubjectID == opts.ExcludeSubjectID {
			continue
		}
		if opts.excludesChangeset(intent.ChangesetID) {
			continue
		}
		if !PatternsOverlap(norm, intent.Pattern) {
			continue
		}
		warnings = append(warnings, OverlapWarning{
			Kind:            "intent",
			ID:              intent.ID,
			Owner:           displayOwner(intent.CreatorName, intent.CreatorSubjectID),
			Pattern:         intent.Pattern,
			Message:         intent.Message,
			ExpiresAt:       intent.ExpiresAt,
			ConflictPattern: norm,
		})
	}

	for _, cs := range openChangesets {
		if opts.excludesChangeset(cs.ID) {
			continue
		}
		matched := OverlappingPaths(norm, cs.AffectedPaths)
		if len(matched) == 0 {
			continue
		}
		warnings = append(warnings, OverlapWarning{
			Kind:            "changeset",
			ID:              cs.ID,
			Owner:           cs.Author,
			Message:         cs.Title,
			MatchedPaths:    matched,
			ConflictPattern: norm,
		})
	}
	return warnings, nil
}

// FormatWarningLine formats an OverlapWarning into a single advisory terminal line.
func FormatWarningLine(w OverlapWarning, now time.Time) string {
	var scope string
	if len(w.MatchedPaths) > 0 {
		noun := "paths"
		if len(w.MatchedPaths) == 1 {
			noun = "path"
		}
		scope = fmt.Sprintf("%d %s (%s)", len(w.MatchedPaths), noun, summarizePaths(w.MatchedPaths))
	} else if w.ConflictPattern != "" {
		scope = fmt.Sprintf("pattern %s", w.ConflictPattern)
	} else {
		scope = "paths"
	}

	owner := strings.TrimSpace(w.Owner)
	if owner == "" {
		owner = "unknown"
	}

	switch w.Kind {
	case "changeset":
		if strings.TrimSpace(w.Message) != "" {
			return fmt.Sprintf("warning: %s overlap open changeset %s by %s: %q", scope, w.ID, owner, w.Message)
		}
		return fmt.Sprintf("warning: %s overlap open changeset %s by %s", scope, w.ID, owner)
	default:
		var meta []string
		if !w.ExpiresAt.IsZero() && !now.IsZero() {
			meta = append(meta, FormatRemaining(w.ExpiresAt.Sub(now))+" left")
		}
		if w.Pattern != "" {
			meta = append(meta, w.Pattern)
		}
		metaStr := ""
		if len(meta) > 0 {
			metaStr = " (" + strings.Join(meta, ", ") + ")"
		}
		if strings.TrimSpace(w.Message) != "" {
			return fmt.Sprintf("warning: %s overlap active intent %s by %s%s: %q", scope, w.ID, owner, metaStr, w.Message)
		}
		return fmt.Sprintf("warning: %s overlap active intent %s by %s%s", scope, w.ID, owner, metaStr)
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

func displayOwner(name, subjectID string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return subjectID
}

func summarizePaths(matched []string) string {
	if len(matched) <= 2 {
		return strings.Join(matched, ", ")
	}
	return fmt.Sprintf("%s, %s, +%d more", matched[0], matched[1], len(matched)-2)
}

func dedupSorted(in []string) []string {
	set := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if _, ok := set[p]; ok {
			continue
		}
		set[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func intersectSets(targetSet map[string]struct{}, pathsList []string) []string {
	if len(pathsList) == 0 || len(targetSet) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, p := range pathsList {
		if _, ok := targetSet[p]; !ok {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func hasGlob(pattern string) bool {
	return segmentHasGlob(pattern)
}

func segmentHasGlob(seg string) bool {
	return strings.ContainsAny(seg, "*?[{")
}

// patternVariants returns the segment-list representations of pattern.
// Non-glob patterns represent both the exact path and everything underneath it
// ("<pattern>/**") so directory intents intersect properly with glob intents.
// Simple top-level brace alternations are expanded up to a bounded limit.
func patternVariants(pattern string) [][]string {
	expanded := expandBraces(pattern, 32)
	var out [][]string
	for _, exp := range expanded {
		segs := splitSegs(exp)
		out = append(out, segs)
		if !hasGlob(exp) {
			withWildcard := slices.Concat(segs, []string{"**"})
			out = append(out, withWildcard)
		}
	}
	return out
}

func splitSegs(p string) []string {
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func expandBraces(p string, limit int) []string {
	results := []string{p}
	for {
		expandedAny := false
		var next []string
		for _, cur := range results {
			start, end, alts, ok := findInnermostBrace(cur)
			if !ok {
				next = append(next, cur)
				continue
			}
			expandedAny = true
			for _, alt := range alts {
				next = append(next, cur[:start]+alt+cur[end+1:])
				if len(next) >= limit {
					return next
				}
			}
		}
		results = next
		if !expandedAny || len(results) >= limit {
			return results
		}
	}
}

func findInnermostBrace(s string) (int, int, []string, bool) {
	openIdx := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			openIdx = i
		case '}':
			if openIdx >= 0 {
				inner := s[openIdx+1 : i]
				return openIdx, i, strings.Split(inner, ","), true
			}
		}
	}
	return 0, 0, nil, false
}

func segListsOverlap(a, b []string) bool {
	type key struct{ i, j int }
	memo := make(map[key]bool)
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		if i == len(a) && j == len(b) {
			return true
		}
		if i < len(a) && a[i] == "**" && j == len(b) {
			return rec(i+1, j)
		}
		if j < len(b) && b[j] == "**" && i == len(a) {
			return rec(i, j+1)
		}
		if i == len(a) || j == len(b) {
			return false
		}

		k := key{i, j}
		if v, seen := memo[k]; seen {
			return v
		}
		var ok bool
		switch {
		case a[i] == "**" && b[j] == "**":
			ok = rec(i+1, j) || rec(i, j+1)
		case a[i] == "**":
			ok = rec(i+1, j) || rec(i, j+1)
		case b[j] == "**":
			ok = rec(i, j+1) || rec(i+1, j)
		default:
			ok = singleSegOverlap(a[i], b[j]) && rec(i+1, j+1)
		}
		memo[k] = ok
		return ok
	}
	return rec(0, 0)
}

type segTok struct {
	kind byte // '*', '?', 'c' (literal), '[' (char class)
	ch   byte
	raw  string // char class inside [...]
}

func tokenizeSingleSeg(s string) []segTok {
	var toks []segTok
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*':
			if len(toks) > 0 && toks[len(toks)-1].kind == '*' {
				continue
			}
			toks = append(toks, segTok{kind: '*'})
		case '?':
			toks = append(toks, segTok{kind: '?'})
		case '[':
			end := i + 1
			if end < len(s) && (s[end] == '!' || s[end] == '^') {
				end++
			}
			if end < len(s) && s[end] == ']' {
				end++
			}
			for end < len(s) && s[end] != ']' {
				end++
			}
			if end < len(s) {
				toks = append(toks, segTok{kind: '[', raw: s[i : end+1]})
				i = end
			} else {
				toks = append(toks, segTok{kind: 'c', ch: s[i]})
			}
		default:
			toks = append(toks, segTok{kind: 'c', ch: s[i]})
		}
	}
	return toks
}

func tokensOverlap(a, b segTok) bool {
	if a.kind == '?' || b.kind == '?' {
		return true
	}
	if a.kind == 'c' && b.kind == 'c' {
		return a.ch == b.ch
	}
	if a.kind == 'c' && b.kind == '[' {
		matched, err := doublestar.Match(b.raw, string(a.ch))
		return err == nil && matched
	}
	if a.kind == '[' && b.kind == 'c' {
		matched, err := doublestar.Match(a.raw, string(b.ch))
		return err == nil && matched
	}
	if a.kind == '[' && b.kind == '[' {
		for ch := byte(0x20); ch <= 0x7e; ch++ {
			if ch == '/' {
				continue
			}
			s := string(ch)
			m1, err1 := doublestar.Match(a.raw, s)
			m2, err2 := doublestar.Match(b.raw, s)
			if err1 == nil && err2 == nil && m1 && m2 {
				return true
			}
		}
		return false
	}
	return false
}

func singleSegOverlap(sa, sb string) bool {
	if sa == sb {
		return true
	}
	ta := tokenizeSingleSeg(sa)
	tb := tokenizeSingleSeg(sb)

	type key struct{ i, j int }
	memo := make(map[key]bool)
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		if i == len(ta) && j == len(tb) {
			return true
		}
		if i < len(ta) && ta[i].kind == '*' && j == len(tb) {
			return rec(i+1, j)
		}
		if j < len(tb) && tb[j].kind == '*' && i == len(ta) {
			return rec(i, j+1)
		}
		if i == len(ta) || j == len(tb) {
			return false
		}

		k := key{i, j}
		if v, seen := memo[k]; seen {
			return v
		}
		var ok bool
		switch {
		case ta[i].kind == '*' && tb[j].kind == '*':
			ok = rec(i+1, j) || rec(i, j+1)
		case ta[i].kind == '*':
			ok = rec(i+1, j) || rec(i, j+1)
		case tb[j].kind == '*':
			ok = rec(i, j+1) || rec(i+1, j)
		default:
			ok = tokensOverlap(ta[i], tb[j]) && rec(i+1, j+1)
		}
		memo[k] = ok
		return ok
	}
	return rec(0, 0)
}
