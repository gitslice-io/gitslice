package diffutil

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

type File struct {
	Path   string
	Exists bool
	Data   []byte
}

// UnifiedFileDiff returns a unified file diff that is always valid UTF-8 so it
// is safe for callers to store in proto3 string fields.
func UnifiedFileDiff(oldFile, newFile File) string {
	if oldFile.Exists && newFile.Exists && bytes.Equal(oldFile.Data, newFile.Data) {
		return ""
	}
	p := newFile.Path
	if p == "" {
		p = oldFile.Path
	}
	label := strings.TrimPrefix(p, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", label, label)
	if !oldFile.Exists && newFile.Exists {
		fmt.Fprintf(&b, "new file mode 100644\n")
	}
	if oldFile.Exists && !newFile.Exists {
		fmt.Fprintf(&b, "deleted file mode 100644\n")
	}
	if oldFile.Exists && isBinary(oldFile.Data) || newFile.Exists && isBinary(newFile.Data) {
		oldLabel := "/dev/null"
		if oldFile.Exists {
			oldLabel = "a/" + label
		}
		newLabel := "/dev/null"
		if newFile.Exists {
			newLabel = "b/" + label
		}
		fmt.Fprintf(&b, "Binary files %s and %s differ\n", oldLabel, newLabel)
	} else {
		if oldFile.Exists {
			fmt.Fprintf(&b, "--- a/%s\n", label)
		} else {
			b.WriteString("--- /dev/null\n")
		}
		if newFile.Exists {
			fmt.Fprintf(&b, "+++ b/%s\n", label)
		} else {
			b.WriteString("+++ /dev/null\n")
		}
		oldLines := splitLines(oldFile.Data)
		newLines := splitLines(newFile.Data)
		fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
		for _, line := range lcsDiff(oldLines, newLines) {
			// Source lines retain their original trailing newline (or lack one for a
			// final line without it), so emit each diff entry on its own line to keep
			// the unified diff well-formed even when a file has no trailing newline.
			b.WriteString(strings.TrimRight(line, "\n"))
			b.WriteByte('\n')
		}
	}
	diff := b.String()
	if !utf8.ValidString(diff) {
		return strings.ToValidUTF8(diff, "�")
	}
	return diff
}

// isBinary reports whether data looks like binary content, using git's
// heuristic: any NUL byte in the first 8000 bytes.
func isBinary(data []byte) bool {
	if len(data) > 8000 {
		data = data[:8000]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func lcsDiff(a, b []string) []string {
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
	var out []string
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			out = append(out, " "+a[i])
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			out = append(out, "-"+a[i])
			i++
		} else {
			out = append(out, "+"+b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, "-"+a[i])
	}
	for ; j < len(b); j++ {
		out = append(out, "+"+b[j])
	}
	return out
}
