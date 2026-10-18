package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// defaultDiffContext is how many unchanged lines gs diff shows around each
// change, as git does.
const defaultDiffContext = 3

// diffContext is how many unchanged lines of context diff output shows around
// a change: -U/--unified, else three. Negative shows the whole file.
func (o commandOptions) diffContext() int {
	if o.DiffContextSet {
		return o.DiffContext
	}
	return defaultDiffContext
}

// addDiffContextFlag adds -U/--unified to a command that prints diffs.
func addDiffContextFlag(cmd *cobra.Command, value *int) {
	cmd.Flags().IntVarP(value, "unified", "U", defaultDiffContext, "unchanged lines of context around each change; a negative number shows the whole file")
}

// diffContextOptions returns opts with the context the command's -U flag chose.
func diffContextOptions(cmd *cobra.Command, opts commandOptions, value int) commandOptions {
	opts.DiffContext = value
	opts.DiffContextSet = cmd.Flags().Changed("unified")
	return opts
}

// compactUnifiedDiff rewrites a diff whose files are each one hunk covering the
// whole file, as diffutil and the server produce them (the web viewer folds the
// unchanged lines itself and needs them all), into the usual hunks: each change
// with context unchanged lines around it, and changes close enough to share
// context in one hunk. Anything else passes through as it is: a negative
// context, binary files, oversized diffs, hunks that already have a partial
// range, and hunks whose line counts do not match their headers.
func compactUnifiedDiff(diff string, context int) string {
	if context < 0 || diff == "" {
		return diff
	}
	lines := strings.SplitAfter(diff, "\n")
	var out strings.Builder
	for i := 0; i < len(lines); {
		header := lines[i]
		if !strings.HasPrefix(header, "@@ -") {
			out.WriteString(header)
			i++
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "diff --git ") {
			end++
		}
		body := lines[i+1 : end]
		if compact, ok := compactWholeFileHunk(header, body, context); ok {
			out.WriteString(compact)
		} else {
			out.WriteString(header)
			for _, line := range body {
				out.WriteString(line)
			}
		}
		i = end
	}
	return out.String()
}

// compactWholeFileHunk compacts one hunk if it covers a whole file.
func compactWholeFileHunk(header string, body []string, context int) (string, bool) {
	oldStart, oldCount, newStart, newCount, ok := parseHunkHeader(header)
	if !ok || oldStart != 1 || newStart != 1 {
		return "", false
	}
	// The body of a file with no lines at the end of the diff text is a lone
	// empty element.
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	kinds := make([]byte, len(body))
	var oldSeen, newSeen int
	firstChange, lastChange := -1, -1
	for i, line := range body {
		if line == "" {
			return "", false
		}
		kinds[i] = line[0]
		switch line[0] {
		case ' ':
			oldSeen++
			newSeen++
		case '-':
			oldSeen++
		case '+':
			newSeen++
		default:
			return "", false
		}
		if line[0] != ' ' {
			if firstChange < 0 {
				firstChange = i
			}
			lastChange = i
		}
	}
	if oldSeen != oldCount || newSeen != newCount || firstChange < 0 {
		return "", false
	}

	// Hunk ranges [from, to): context around each change, merged where the
	// contexts touch.
	type span struct{ from, to int }
	var spans []span
	for i := firstChange; i <= lastChange; i++ {
		if kinds[i] == ' ' {
			continue
		}
		from, to := max(i-context, 0), min(i+context+1, len(body))
		if n := len(spans); n > 0 && from <= spans[n-1].to {
			spans[n-1].to = max(spans[n-1].to, to)
			continue
		}
		spans = append(spans, span{from, to})
	}

	var b strings.Builder
	oldBefore, newBefore := 0, 0 // old and new lines before the span
	next := 0                    // body index oldBefore and newBefore are counted up to
	for _, s := range spans {
		for ; next < s.from; next++ {
			if kinds[next] != '+' {
				oldBefore++
			}
			if kinds[next] != '-' {
				newBefore++
			}
		}
		oldN, newN := 0, 0
		for i := s.from; i < s.to; i++ {
			if kinds[i] != '+' {
				oldN++
			}
			if kinds[i] != '-' {
				newN++
			}
		}
		// A side with no lines in the hunk starts at the line before it.
		oldFrom, newFrom := oldBefore+1, newBefore+1
		if oldN == 0 {
			oldFrom = oldBefore
		}
		if newN == 0 {
			newFrom = newBefore
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldFrom, oldN, newFrom, newN)
		for i := s.from; i < s.to; i++ {
			b.WriteString(body[i])
			if !strings.HasSuffix(body[i], "\n") {
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), true
}

// parseHunkHeader reads "@@ -a,b +c,d @@" (a count of 1 may be left out).
func parseHunkHeader(line string) (oldStart, oldCount, newStart, newCount int, ok bool) {
	line = strings.TrimSuffix(line, "\n")
	rest, found := strings.CutPrefix(line, "@@ -")
	if !found {
		return 0, 0, 0, 0, false
	}
	oldRange, rest, found := strings.Cut(rest, " +")
	if !found {
		return 0, 0, 0, 0, false
	}
	newRange, _, found := strings.Cut(rest, " @@")
	if !found {
		return 0, 0, 0, 0, false
	}
	var errOld, errNew error
	oldStart, oldCount, errOld = parseHunkRange(oldRange)
	newStart, newCount, errNew = parseHunkRange(newRange)
	return oldStart, oldCount, newStart, newCount, errOld == nil && errNew == nil
}

func parseHunkRange(s string) (start, count int, err error) {
	first, second, hasCount := strings.Cut(s, ",")
	if start, err = strconv.Atoi(first); err != nil {
		return 0, 0, err
	}
	if !hasCount {
		return start, 1, nil
	}
	count, err = strconv.Atoi(second)
	return start, count, err
}
