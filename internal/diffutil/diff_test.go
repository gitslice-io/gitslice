package diffutil

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnifiedFileDiffMatchesFullLCSReference(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "identical files short circuit", old: "a\nb\n", new: "a\nb\n"},
		{name: "insertion at top", old: "a\nb\n", new: "top\na\nb\n"},
		{name: "insertion in middle", old: "a\nb\n", new: "a\nmiddle\nb\n"},
		{name: "insertion at bottom", old: "a\nb\n", new: "a\nb\nbottom\n"},
		{name: "pure deletion", old: "a\ndeleted\nb\n", new: "a\nb\n"},
		{name: "change between shared prefix and suffix", old: "prefix\nold one\nold two\nsuffix\n", new: "prefix\nnew one\nnew two\nsuffix\n"},
		{name: "completely disjoint", old: "a\nb\nc\n", new: "x\ny\nz\n"},
		{name: "empty old", old: "", new: "a\nb\n"},
		{name: "empty new", old: "a\nb\n", new: ""},
		{name: "repeated blank prefix and suffix", old: "\n\nold\n\n\n", new: "\n\nnew\n\n\n"},
		{name: "no trailing newline in either file", old: "prefix\nold\nsuffix", new: "prefix\nnew\nsuffix"},
		{name: "trailing newline added", old: "a\nb", new: "a\nb\n"},
		{name: "trailing newline removed", old: "a\nb\n", new: "a\nb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldFile := File{Path: "/test.txt", Exists: true, Data: []byte(tt.old)}
			newFile := File{Path: "/test.txt", Exists: true, Data: []byte(tt.new)}
			got := UnifiedFileDiff(oldFile, newFile)
			want := unifiedFileDiffReference(oldFile, newFile)
			if got != want {
				t.Fatalf("UnifiedFileDiff() differs from full LCS reference\ngot:  %q\nwant: %q", got, want)
			}
		})
	}
}

func TestUnifiedFileDiffTooLargeAfterTrimming(t *testing.T) {
	originalMaxLCSCells := maxLCSCells
	maxLCSCells = 100
	t.Cleanup(func() { maxLCSCells = originalMaxLCSCells })

	const lineCount = 2002
	var oldData, newData strings.Builder
	for i := 0; i < lineCount; i++ {
		fmt.Fprintf(&oldData, "old-%04d\n", i)
		fmt.Fprintf(&newData, "new-%04d\n", i)
	}

	got := UnifiedFileDiff(
		File{Path: "/large.txt", Exists: true, Data: []byte(oldData.String())},
		File{Path: "/large.txt", Exists: true, Data: []byte(newData.String())},
	)
	want := "diff --git a/large.txt b/large.txt\n" +
		"Diff too large to render: a/large.txt and b/large.txt differ (2002 -> 2002 lines)\n"
	if got != want {
		t.Fatalf("UnifiedFileDiff() oversized result mismatch\ngot:  %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "@@") || strings.Contains(got, "---") || strings.Contains(got, "+++") {
		t.Fatalf("oversized diff contains text diff headers:\n%s", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("oversized diff is not valid UTF-8: %q", got)
	}
}

func TestUnifiedFileDiffTrimsBeforeCellCap(t *testing.T) {
	const sharedLinesPerSide = 5000
	var oldData, newData, want strings.Builder
	for i := 0; i < sharedLinesPerSide; i++ {
		oldData.WriteString("shared\n")
		newData.WriteString("shared\n")
	}
	oldData.WriteString("old one\nold two\nold three\n")
	newData.WriteString("new one\nnew two\nnew three\n")
	for i := 0; i < sharedLinesPerSide; i++ {
		oldData.WriteString("shared\n")
		newData.WriteString("shared\n")
	}

	want.WriteString("diff --git a/large-middle.txt b/large-middle.txt\n")
	want.WriteString("--- a/large-middle.txt\n")
	want.WriteString("+++ b/large-middle.txt\n")
	want.WriteString("@@ -1,10003 +1,10003 @@\n")
	for i := 0; i < sharedLinesPerSide; i++ {
		want.WriteString(" shared\n")
	}
	want.WriteString("-old one\n-old two\n-old three\n")
	want.WriteString("+new one\n+new two\n+new three\n")
	for i := 0; i < sharedLinesPerSide; i++ {
		want.WriteString(" shared\n")
	}

	got := UnifiedFileDiff(
		File{Path: "/large-middle.txt", Exists: true, Data: []byte(oldData.String())},
		File{Path: "/large-middle.txt", Exists: true, Data: []byte(newData.String())},
	)
	if got != want.String() {
		t.Fatalf("UnifiedFileDiff() did not render the trimmed large diff correctly")
	}
}

func unifiedFileDiffReference(oldFile, newFile File) string {
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
		b.WriteString("new file mode 100644\n")
	}
	if oldFile.Exists && !newFile.Exists {
		b.WriteString("deleted file mode 100644\n")
	}
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
	for _, line := range lcsDiffReference(oldLines, newLines) {
		b.WriteString(strings.TrimRight(line, "\n"))
		b.WriteByte('\n')
	}
	diff := b.String()
	if !utf8.ValidString(diff) {
		return strings.ToValidUTF8(diff, "�")
	}
	return diff
}

func lcsDiffReference(a, b []string) []string {
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
