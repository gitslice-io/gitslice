package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

func printPathList(w io.Writer, paths []string) {
	for _, p := range paths {
		fmt.Fprintln(w, p)
	}
}

func printPathStat(w io.Writer, paths []string) {
	fmt.Fprintf(w, "%d changed path(s)\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(w, "  %s\n", p)
	}
}

func printIndentedPaths(w io.Writer, paths []string, indent string) {
	if len(paths) == 0 {
		fmt.Fprintln(w, indent+"none")
		return
	}
	for _, p := range paths {
		fmt.Fprintln(w, indent+p)
	}
}

func printPathListColor(w io.Writer, paths []string, color bool) {
	for _, p := range paths {
		fmt.Fprintln(w, colorize(color, ansiBlue, p))
	}
}

func printPathStatColor(w io.Writer, paths []string, color bool) {
	fmt.Fprintf(w, "%d changed path(s)\n", len(paths))
	printIndentedPathsColor(w, paths, "  ", color)
}

// printUnifiedDiffStatColor prints a compact per-file diffstat with added and
// removed line counts derived from unified diff text, falling back to plain
// path names for entries without diff hunks (e.g. truncated diffs).
func printUnifiedDiffStatColor(w io.Writer, paths []string, diffText string, color bool) {
	changes := parseUnifiedDiffStats(paths, diffText)
	if len(changes) == 0 {
		printPathStatColor(w, paths, color)
		return
	}
	fmt.Fprintln(w, changeHeadline(changes))
	printChangeSummary(w, changes, "  ", color)
}

func parseUnifiedDiffStats(paths []string, diffText string) []pathChange {
	byPath := map[string]*pathChange{}
	order := make([]string, 0, len(paths))
	ensure := func(p string) *pathChange {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if existing, ok := byPath[p]; ok {
			return existing
		}
		c := &pathChange{Path: p, Kind: "modified"}
		byPath[p] = c
		order = append(order, p)
		return c
	}
	for _, p := range paths {
		ensure(p)
	}
	var cur *pathChange
	inHunk := false
	for _, line := range strings.Split(diffText, "\n") {
		if strings.HasPrefix(line, "diff --git a/") {
			inHunk = false
			rest := strings.TrimPrefix(line, "diff --git a/")
			if idx := strings.LastIndex(rest, " b/"); idx >= 0 {
				p := rest[idx+3:]
				cur = ensure(p)
			} else {
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "new file mode "):
			cur.Kind = "added"
		case strings.HasPrefix(line, "deleted file mode "):
			cur.Kind = "deleted"
		case strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
			cur.Counted = false
			inHunk = false
		case strings.HasPrefix(line, "Diff too large to render:"):
			cur.Counted = false
			inHunk = false
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
			if !cur.Binary {
				cur.Counted = true
			}
		case inHunk && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++ "):
			cur.Added++
		case inHunk && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--- "):
			cur.Removed++
		}
	}
	if len(order) == 0 {
		return nil
	}
	sort.Strings(order)
	out := make([]pathChange, 0, len(order))
	for _, p := range order {
		out = append(out, *byPath[p])
	}
	return out
}

func printIndentedPathsColor(w io.Writer, paths []string, indent string, color bool) {
	if len(paths) == 0 {
		fmt.Fprintln(w, indent+"none")
		return
	}
	for _, p := range paths {
		fmt.Fprintln(w, indent+colorize(color, ansiBlue, p))
	}
}
