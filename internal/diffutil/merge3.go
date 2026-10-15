package diffutil

import (
	"bytes"
	"strings"
)

// Merge3 merges two versions of a text file that both changed base: changes
// to different lines are combined; where both sides changed the same lines
// (or lines next to each other) differently, the region is written with
// conflict markers, ours first, and clean is false. Changes are found with
// MatchLines, so the merge needs memory in proportion to the edits, not to
// the file's size squared.
func Merge3(base, ours, theirs []byte, oursLabel, theirsLabel string) (merged []byte, clean bool) {
	switch {
	case bytes.Equal(ours, theirs), bytes.Equal(base, theirs):
		return ours, true
	case bytes.Equal(base, ours):
		return theirs, true
	}
	baseLines := splitLines(base)
	oursHunks := lineHunks(baseLines, splitLines(ours))
	theirsHunks := lineHunks(baseLines, splitLines(theirs))

	var out []string
	clean = true
	pos, i, j := 0, 0, 0
	for i < len(oursHunks) || j < len(theirsHunks) {
		// A cluster starts at the next hunk and takes in every hunk from the
		// other side that overlaps or touches it.
		var start, end int
		fromOurs, fromTheirs := i, j
		if j >= len(theirsHunks) || (i < len(oursHunks) && oursHunks[i].start <= theirsHunks[j].start) {
			start, end = oursHunks[i].start, oursHunks[i].end
			i++
		} else {
			start, end = theirsHunks[j].start, theirsHunks[j].end
			j++
		}
		for {
			hasOurs, hasTheirs := i > fromOurs, j > fromTheirs
			grew := false
			if i < len(oursHunks) && (oursHunks[i].start < end || (oursHunks[i].start == end && hasTheirs)) {
				end = max(end, oursHunks[i].end)
				i++
				grew = true
			}
			if j < len(theirsHunks) && (theirsHunks[j].start < end || (theirsHunks[j].start == end && hasOurs)) {
				end = max(end, theirsHunks[j].end)
				j++
				grew = true
			}
			if !grew {
				break
			}
		}
		out = append(out, baseLines[pos:start]...)
		oursSide := applyHunks(baseLines, start, end, oursHunks[fromOurs:i])
		theirsSide := applyHunks(baseLines, start, end, theirsHunks[fromTheirs:j])
		switch {
		case i == fromOurs:
			out = append(out, theirsSide...)
		case j == fromTheirs, linesEqual(oursSide, theirsSide):
			out = append(out, oursSide...)
		default:
			clean = false
			out = append(out, "<<<<<<< "+oursLabel+"\n")
			out = append(out, terminated(oursSide)...)
			out = append(out, "=======\n")
			out = append(out, terminated(theirsSide)...)
			out = append(out, ">>>>>>> "+theirsLabel+"\n")
		}
		pos = end
	}
	out = append(out, baseLines[pos:]...)
	return []byte(strings.Join(out, "")), clean
}

// lineHunk replaces base[start:end] with lines.
type lineHunk struct {
	start, end int
	lines      []string
}

// lineHunks lists other's changes to base, in base order.
func lineHunks(base, other []string) []lineHunk {
	match := MatchLines(base, other, 0)
	var out []lineHunk
	baseNext, otherNext := 0, 0
	for j := 0; j <= len(other); j++ {
		if j < len(other) && match[j] < 0 {
			continue
		}
		target := len(base)
		if j < len(other) {
			target = match[j]
		}
		if target > baseNext || j > otherNext {
			out = append(out, lineHunk{start: baseNext, end: target, lines: other[otherNext:j]})
		}
		baseNext, otherNext = target+1, j+1
	}
	return out
}

func applyHunks(base []string, start, end int, hunks []lineHunk) []string {
	var out []string
	pos := start
	for _, h := range hunks {
		out = append(out, base[pos:h.start]...)
		out = append(out, h.lines...)
		pos = h.end
	}
	return append(out, base[pos:end]...)
}

// terminated ends the last line with a newline, so a conflict marker after it
// starts its own line.
func terminated(lines []string) []string {
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines = append(append([]string(nil), lines[:n-1]...), lines[n-1]+"\n")
	}
	return lines
}

func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
