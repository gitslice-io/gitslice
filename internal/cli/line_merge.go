package cli

import (
	"bytes"
	"strings"
)

type lineChange struct {
	baseStart   int
	baseEnd     int
	replacement []string
}

type lineDiffOp struct {
	kind string
	line string
}

func mergeTextLines(baseData, localData, remoteData []byte) ([]byte, bool) {
	switch {
	case bytes.Equal(localData, remoteData):
		return localData, true
	case bytes.Equal(baseData, localData):
		return remoteData, true
	case bytes.Equal(baseData, remoteData):
		return localData, true
	}
	baseLines := splitMergeLines(baseData)
	localChanges := diffLineChanges(baseLines, splitMergeLines(localData))
	remoteChanges := diffLineChanges(baseLines, splitMergeLines(remoteData))
	mergedLines, ok := mergeLineChanges(baseLines, localChanges, remoteChanges)
	if !ok {
		return nil, false
	}
	return []byte(strings.Join(mergedLines, "")), true
}

func splitMergeLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func diffLineChanges(base, target []string) []lineChange {
	ops := lcsLineDiff(base, target)
	changes := []lineChange{}
	baseIndex := 0
	active := false
	current := lineChange{}
	flush := func() {
		if !active {
			return
		}
		current.baseEnd = baseIndex
		changes = append(changes, current)
		active = false
		current = lineChange{}
	}
	for _, op := range ops {
		switch op.kind {
		case "=":
			flush()
			baseIndex++
		case "-":
			if !active {
				current = lineChange{baseStart: baseIndex}
				active = true
			}
			baseIndex++
		case "+":
			if !active {
				current = lineChange{baseStart: baseIndex}
				active = true
			}
			current.replacement = append(current.replacement, op.line)
		}
	}
	flush()
	return changes
}

func lcsLineDiff(a, b []string) []lineDiffOp {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := []lineDiffOp{}
	for i, j := 0, 0; i < len(a) || j < len(b); {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			ops = append(ops, lineDiffOp{kind: "=", line: a[i]})
			i++
			j++
		case i < len(a) && (j == len(b) || dp[i+1][j] >= dp[i][j+1]):
			ops = append(ops, lineDiffOp{kind: "-", line: a[i]})
			i++
		case j < len(b):
			ops = append(ops, lineDiffOp{kind: "+", line: b[j]})
			j++
		}
	}
	return ops
}

func mergeLineChanges(base []string, localChanges, remoteChanges []lineChange) ([]string, bool) {
	out := []string{}
	baseIndex := 0
	i, j := 0, 0
	appendChange := func(change lineChange) bool {
		if change.baseStart < baseIndex {
			return false
		}
		out = append(out, base[baseIndex:change.baseStart]...)
		out = append(out, change.replacement...)
		baseIndex = change.baseEnd
		return true
	}
	for i < len(localChanges) || j < len(remoteChanges) {
		switch {
		case i >= len(localChanges):
			if !appendChange(remoteChanges[j]) {
				return nil, false
			}
			j++
		case j >= len(remoteChanges):
			if !appendChange(localChanges[i]) {
				return nil, false
			}
			i++
		case sameLineChange(localChanges[i], remoteChanges[j]):
			if !appendChange(localChanges[i]) {
				return nil, false
			}
			i++
			j++
		case lineChangeBefore(localChanges[i], remoteChanges[j]):
			if !appendChange(localChanges[i]) {
				return nil, false
			}
			i++
		case lineChangeBefore(remoteChanges[j], localChanges[i]):
			if !appendChange(remoteChanges[j]) {
				return nil, false
			}
			j++
		default:
			return nil, false
		}
	}
	out = append(out, base[baseIndex:]...)
	return out, true
}

func sameLineChange(a, b lineChange) bool {
	return a.baseStart == b.baseStart && a.baseEnd == b.baseEnd && stringSlicesEqual(a.replacement, b.replacement)
}

func lineChangeBefore(a, b lineChange) bool {
	if a.baseEnd < b.baseStart {
		return true
	}
	if a.baseEnd == b.baseStart {
		return !(a.baseStart == a.baseEnd && b.baseStart == b.baseEnd)
	}
	return false
}

func stringSlicesEqual(a, b []string) bool {
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
