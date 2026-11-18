package intents

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/paths"
)

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
