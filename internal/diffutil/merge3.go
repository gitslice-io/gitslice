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

	// First find the clusters: runs of hunks from either side that overlap
	// or touch.
	var clusters []mergeCluster
	i, j := 0, 0
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
		c := mergeCluster{start: start, end: end, ours: oursHunks[fromOurs:i], theirs: theirsHunks[fromTheirs:j]}
		c.resolve(baseLines)
		clusters = append(clusters, c)
	}

	// A change next to a conflict usually belongs to the same edit (both
	// sides rewrote one block, but each kept a line or two of it). Applying
	// it outside the markers can leave half of one side's version in the
	// file unmarked, such as a declaration that the other side also makes.
	// So a conflict takes in every change within conflictContext unchanged
	// lines of it, and the whole region is shown inside the markers.
	for k := 0; k < len(clusters); k++ {
		if !clusters[k].conflict {
			continue
		}
		for k > 0 && clusters[k].start-clusters[k-1].end < conflictContext {
			clusters[k-1].absorb(clusters[k], baseLines)
			clusters = append(clusters[:k], clusters[k+1:]...)
			k--
		}
		for k+1 < len(clusters) && clusters[k+1].start-clusters[k].end < conflictContext {
			clusters[k].absorb(clusters[k+1], baseLines)
			clusters = append(clusters[:k+1], clusters[k+2:]...)
		}
	}

	var out []string
	clean = true
	pos := 0
	for _, c := range clusters {
		out = append(out, baseLines[pos:c.start]...)
		switch {
		case len(c.ours) == 0:
			out = append(out, c.theirsSide...)
		case !c.conflict:
			out = append(out, c.oursSide...)
		default:
			clean = false
			out = append(out, "<<<<<<< "+oursLabel+"\n")
			out = append(out, terminated(c.oursSide)...)
			out = append(out, "=======\n")
			out = append(out, terminated(c.theirsSide)...)
			out = append(out, ">>>>>>> "+theirsLabel+"\n")
		}
		pos = c.end
	}
	out = append(out, baseLines[pos:]...)
	return []byte(strings.Join(out, "")), clean
}

// conflictContext is how many unchanged lines must separate a conflict from
// another change for that change to be applied on its own, outside the
// markers. It matches the context git shows around a hunk.
const conflictContext = 3

// mergeCluster is a region base[start:end] that one or both sides changed.
type mergeCluster struct {
	start, end           int
	ours, theirs         []lineHunk
	oursSide, theirsSide []string
	conflict             bool
}

// resolve works out what each side made of the region and whether they
// disagree.
func (c *mergeCluster) resolve(base []string) {
	c.oursSide = applyHunks(base, c.start, c.end, c.ours)
	c.theirsSide = applyHunks(base, c.start, c.end, c.theirs)
	c.conflict = len(c.ours) > 0 && len(c.theirs) > 0 && !linesEqual(c.oursSide, c.theirsSide)
}

// absorb extends c over next, the cluster after it, and the unchanged lines
// between them. The result is a conflict when c was one: both sides of the
// joined region are shown, each with only its own changes.
func (c *mergeCluster) absorb(next mergeCluster, base []string) {
	wasConflict := c.conflict || next.conflict
	c.end = next.end
	c.ours = append(append([]lineHunk(nil), c.ours...), next.ours...)
	c.theirs = append(append([]lineHunk(nil), c.theirs...), next.theirs...)
	c.resolve(base)
	c.conflict = wasConflict && !linesEqual(c.oursSide, c.theirsSide)
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
