package diffutil

// LineStats counts the lines a change from old to new adds and removes, as a
// unified diff of them would show. binary is set, and nothing counted, when
// either side is binary. A change too large for the line matcher counts its
// whole differing middle as removed and added.
func LineStats(old, new []byte) (added, removed int, binary bool) {
	if isBinary(old) || isBinary(new) {
		return 0, 0, true
	}
	a, b := splitLines(old), splitLines(new)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	aEnd, bEnd := len(a), len(b)
	for aEnd > prefix && bEnd > prefix && a[aEnd-1] == b[bEnd-1] {
		aEnd--
		bEnd--
	}
	a, b = a[prefix:aEnd], b[prefix:bEnd]
	if len(a) == 0 || len(b) == 0 || len(a)*len(b) > maxLCSCells {
		return len(b), len(a), false
	}
	common := commonLineCount(a, b)
	return len(b) - common, len(a) - common, false
}

// commonLineCount is the length of the longest common subsequence of a and b, in
// O(len(b)) memory.
func commonLineCount(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				cur[j] = prev[j+1] + 1
			case prev[j] >= cur[j+1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j+1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[0]
}
