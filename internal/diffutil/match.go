package diffutil

// MatchLines pairs the lines of newLines with the lines of oldLines they are
// unchanged from: match[j] is the index in oldLines of new line j, or -1 when
// line j was added or changed. It finds a shortest edit script (Myers), after
// trimming the common prefix and suffix, in memory proportional to the square
// of the number of edits. When a region needs more than maxEdits edits it is
// treated as rewritten (no matches), which keeps a blame of a huge rewrite
// bounded; maxEdits <= 0 means the default.
func MatchLines(oldLines, newLines []string, maxEdits int) []int {
	if maxEdits <= 0 {
		maxEdits = defaultMaxEdits
	}
	match := make([]int, len(newLines))
	for j := range match {
		match[j] = -1
	}
	// Compare small integers rather than strings.
	ids := map[string]int{}
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, line := range lines {
			id, ok := ids[line]
			if !ok {
				id = len(ids)
				ids[line] = id
			}
			out[i] = id
		}
		return out
	}
	a, b := intern(oldLines), intern(newLines)

	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		match[prefix] = prefix
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		match[len(b)-1-suffix] = len(a) - 1 - suffix
		suffix++
	}
	middleA, middleB := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	for _, pair := range myersPairs(middleA, middleB, maxEdits) {
		match[prefix+pair[1]] = prefix + pair[0]
	}
	return match
}

const defaultMaxEdits = 4000

// myersPairs returns the (old, new) index pairs of a shortest edit script's
// unchanged lines, or none when it needs more than maxEdits edits.
func myersPairs(a, b []int, maxEdits int) [][2]int {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}
	limit := min(n+m, maxEdits)
	// v[k+offset] is the furthest x reached on diagonal k; trace keeps the
	// window [-d, d] of v after each round d, for walking back.
	offset := limit + 1
	v := make([]int, 2*limit+3)
	var trace [][]int
	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+offset] < v[k+1+offset]) {
				x = v[k+1+offset]
			} else {
				x = v[k-1+offset] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+offset] = x
			if x >= n && y >= m {
				trace = append(trace, append([]int(nil), v[offset-d:offset+d+1]...))
				return backtrack(trace, n, m)
			}
		}
		trace = append(trace, append([]int(nil), v[offset-d:offset+d+1]...))
	}
	return nil
}

// backtrack walks the trace from (n, m) back to (0, 0), collecting the
// diagonal moves (unchanged lines).
func backtrack(trace [][]int, n, m int) [][2]int {
	var pairs [][2]int
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		k := x - y
		at := func(round, diagonal int) int { return trace[round][diagonal+round] }
		var prevK int
		if d == 0 {
			prevK = 0
		} else if k == -d || (k != d && at(d-1, k-1) < at(d-1, k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := 0
		if d > 0 {
			prevX = at(d-1, prevK)
		}
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			pairs = append(pairs, [2]int{x, y})
		}
		if d > 0 {
			x, y = prevX, prevY
		}
	}
	return pairs
}
