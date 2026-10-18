package cli

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/diffutil"
)

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func fileDiff(path, oldData, newData string, oldExists, newExists bool) string {
	return diffutil.UnifiedFileDiff(
		diffutil.File{Path: path, Exists: oldExists, Data: []byte(oldData)},
		diffutil.File{Path: path, Exists: newExists, Data: []byte(newData)},
	)
}

func TestCompactUnifiedDiffGolden(t *testing.T) {
	old20 := numbered(20)
	edit := func(lines map[int]string) string {
		var b strings.Builder
		for i := 1; i <= 20; i++ {
			if text, ok := lines[i]; ok {
				if text != "" {
					b.WriteString(text + "\n")
				}
				continue
			}
			fmt.Fprintf(&b, "line %d\n", i)
		}
		return b.String()
	}
	header := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n"

	for _, tc := range []struct {
		name    string
		diff    string
		context int
		want    string
	}{
		{
			name:    "one line in the middle",
			diff:    fileDiff("/f.txt", old20, edit(map[int]string{10: "line ten"}), true, true),
			context: 3,
			want:    header + "@@ -7,7 +7,7 @@\n line 7\n line 8\n line 9\n-line 10\n+line ten\n line 11\n line 12\n line 13\n",
		},
		{
			name:    "a change at the top and one at the bottom",
			diff:    fileDiff("/f.txt", old20, edit(map[int]string{1: "first", 20: "last"}), true, true),
			context: 2,
			want:    header + "@@ -1,3 +1,3 @@\n-line 1\n+first\n line 2\n line 3\n@@ -18,3 +18,3 @@\n line 18\n line 19\n-line 20\n+last\n",
		},
		{
			name:    "changes whose context meets share a hunk",
			diff:    fileDiff("/f.txt", old20, edit(map[int]string{5: "five", 9: "nine"}), true, true),
			context: 2,
			want:    header + "@@ -3,9 +3,9 @@\n line 3\n line 4\n-line 5\n+five\n line 6\n line 7\n line 8\n-line 9\n+nine\n line 10\n line 11\n",
		},
		{
			name:    "no context",
			diff:    fileDiff("/f.txt", old20, edit(map[int]string{4: "four", 5: ""}), true, true),
			context: 0,
			want:    header + "@@ -4,2 +4,1 @@\n-line 4\n-line 5\n+four\n",
		},
		{
			name:    "a line added",
			diff:    fileDiff("/f.txt", "a\nb\nc\n", "a\nb\nnew\nc\n", true, true),
			context: 1,
			want:    header + "@@ -2,2 +2,3 @@\n b\n+new\n c\n",
		},
		{
			name:    "a file added",
			diff:    fileDiff("/f.txt", "", "x\ny\n", false, true),
			context: 3,
			want:    "diff --git a/f.txt b/f.txt\nnew file mode 100644\n--- /dev/null\n+++ b/f.txt\n@@ -0,0 +1,2 @@\n+x\n+y\n",
		},
		{
			name:    "a file deleted",
			diff:    fileDiff("/f.txt", "x\ny\n", "", true, false),
			context: 3,
			want:    "diff --git a/f.txt b/f.txt\ndeleted file mode 100644\n--- a/f.txt\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-x\n-y\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactUnifiedDiff(tc.diff, tc.context); got != tc.want {
				t.Fatalf("compactUnifiedDiff(context %d)\ngot:\n%s\nwant:\n%s", tc.context, got, tc.want)
			}
		})
	}
}

func TestCompactUnifiedDiffLeavesOtherDiffsAlone(t *testing.T) {
	whole := fileDiff("/a.txt", numbered(12), strings.Replace(numbered(12), "line 6", "six", 1), true, true)
	binary := diffutil.UnifiedFileDiff(
		diffutil.File{Path: "/img.bin", Exists: true, Data: []byte{0, 1, 2}},
		diffutil.File{Path: "/img.bin", Exists: true, Data: []byte{0, 1, 3}},
	)
	already := "diff --git a/p.txt b/p.txt\n--- a/p.txt\n+++ b/p.txt\n@@ -5,3 +5,3 @@\n a\n-b\n+c\n d\n"
	mismatched := "diff --git a/q.txt b/q.txt\n--- a/q.txt\n+++ b/q.txt\n@@ -1,9 +1,9 @@\n a\n-b\n+c\n"

	if got := compactUnifiedDiff(whole, -1); got != whole {
		t.Error("a negative context must show the whole file")
	}
	for name, diff := range map[string]string{"binary": binary, "already compact": already, "counts that do not match": mismatched, "empty": ""} {
		if got := compactUnifiedDiff(diff, 3); got != diff {
			t.Errorf("%s: changed\ngot:\n%s\nwant:\n%s", name, got, diff)
		}
	}
	// Files in one diff are compacted one by one, whatever is between them.
	both := binary + whole + already
	got := compactUnifiedDiff(both, 1)
	if !strings.HasPrefix(got, binary) || !strings.HasSuffix(got, already) {
		t.Fatalf("the files around a compacted one changed:\n%s", got)
	}
	if !strings.Contains(got, "@@ -5,3 +5,3 @@\n line 5\n-line 6\n+six\n line 7\n") {
		t.Fatalf("the whole-file diff in the middle was not compacted:\n%s", got)
	}
}

type parsedHunk struct {
	oldStart, oldCount, newStart, newCount int
	lines                                  []string
}

func parseHunks(t *testing.T, diff string) []parsedHunk {
	t.Helper()
	var hunks []parsedHunk
	for _, line := range strings.SplitAfter(diff, "\n") {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "@@ -"):
			h := parsedHunk{}
			h.oldStart, h.oldCount, h.newStart, h.newCount, _ = parseHunkHeader(line)
			hunks = append(hunks, h)
		case len(hunks) > 0 && line != "":
			hunks[len(hunks)-1].lines = append(hunks[len(hunks)-1].lines, line)
		}
	}
	return hunks
}

// applyHunks applies parsed hunks to old, checking every line they claim to
// know and each header's counts and start lines.
func applyHunks(t *testing.T, old []string, hunks []parsedHunk) []string {
	t.Helper()
	var out []string
	pos := 0 // lines of old consumed
	for n, h := range hunks {
		wantStart := pos + 1
		consumed := h.oldStart - 1
		if h.oldCount == 0 {
			consumed = h.oldStart
		}
		if consumed < pos {
			t.Fatalf("hunk %d starts at old line %d, inside the previous hunk", n, h.oldStart)
		}
		_ = wantStart
		out = append(out, old[pos:consumed]...)
		pos = consumed
		oldN, newN := 0, 0
		for _, line := range h.lines {
			switch line[0] {
			case ' ':
				if pos >= len(old) || old[pos] != line[1:] {
					t.Fatalf("hunk %d: context %q does not match old line %d", n, line[1:], pos+1)
				}
				out = append(out, line[1:])
				pos++
				oldN++
				newN++
			case '-':
				if pos >= len(old) || old[pos] != line[1:] {
					t.Fatalf("hunk %d: removed %q does not match old line %d", n, line[1:], pos+1)
				}
				pos++
				oldN++
			case '+':
				out = append(out, line[1:])
				newN++
			}
		}
		if oldN != h.oldCount || newN != h.newCount {
			t.Fatalf("hunk %d header says -%d +%d, body has -%d +%d", n, h.oldCount, h.newCount, oldN, newN)
		}
		newStart := len(out) - newN + 1
		if h.newCount == 0 {
			newStart = len(out)
		}
		if h.newStart != newStart {
			t.Fatalf("hunk %d: new start %d, want %d", n, h.newStart, newStart)
		}
	}
	return append(out, old[pos:]...)
}

func TestCompactUnifiedDiffAppliesBack(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []string{"alpha", "beta", "gamma", "delta", "", "alpha beta", "}"}
	lines := func(data string) []string {
		if data == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	}
	for trial := 0; trial < 400; trial++ {
		oldLines := make([]string, rng.Intn(45))
		for i := range oldLines {
			oldLines[i] = alphabet[rng.Intn(len(alphabet))]
		}
		newLines := append([]string(nil), oldLines...)
		for edits := rng.Intn(6); edits > 0; edits-- {
			at := 0
			if len(newLines) > 0 {
				at = rng.Intn(len(newLines) + 1)
			}
			switch kind := rng.Intn(3); {
			case kind == 0 || at >= len(newLines):
				newLines = append(newLines[:at], append([]string{fmt.Sprintf("added %d", rng.Intn(1000))}, newLines[at:]...)...)
			case kind == 1:
				newLines = append(newLines[:at], newLines[at+1:]...)
			default:
				newLines[at] = fmt.Sprintf("changed %d", rng.Intn(1000))
			}
		}
		join := func(l []string) string {
			if len(l) == 0 {
				return ""
			}
			return strings.Join(l, "\n") + "\n"
		}
		oldData, newData := join(oldLines), join(newLines)
		if oldData == newData {
			continue
		}
		whole := fileDiff("/f.txt", oldData, newData, true, true)
		for _, context := range []int{0, 1, 3, 10} {
			compact := compactUnifiedDiff(whole, context)
			hunks := parseHunks(t, compact)
			if got := join(applyHunks(t, lines(oldData), hunks)); got != newData {
				t.Fatalf("trial %d, context %d: applying the compact diff gives\n%q\nwant\n%q\ndiff:\n%s", trial, context, got, newData, compact)
			}
			for n, h := range hunks {
				lead, trail := 0, 0
				for i := 0; i < len(h.lines) && h.lines[i][0] == ' '; i++ {
					lead++
				}
				for i := len(h.lines) - 1; i >= 0 && h.lines[i][0] == ' '; i-- {
					trail++
				}
				if lead > context || trail > context {
					t.Fatalf("trial %d, context %d: hunk %d has %d leading and %d trailing context lines", trial, context, n, lead, trail)
				}
				// Hunks are only separate when more than two contexts of unchanged
				// lines lie between their changes.
				if n > 0 {
					prev := hunks[n-1]
					gap := h.oldStart - (prev.oldStart + prev.oldCount)
					if h.oldCount == 0 {
						gap = h.oldStart + 1 - (prev.oldStart + prev.oldCount)
					}
					if gap+trailingContext(prev)+lead <= 2*context {
						t.Fatalf("trial %d, context %d: hunks %d and %d should be one", trial, context, n-1, n)
					}
				}
			}
			if again := compactUnifiedDiff(compact, context); again != compact {
				t.Fatalf("trial %d, context %d: compacting twice changed the diff", trial, context)
			}
		}
	}
}

func trailingContext(h parsedHunk) int {
	n := 0
	for i := len(h.lines) - 1; i >= 0 && h.lines[i][0] == ' '; i-- {
		n++
	}
	return n
}

func TestDiffContextDefaultsToThree(t *testing.T) {
	if got := (commandOptions{}).diffContext(); got != 3 {
		t.Fatalf("default context = %d", got)
	}
	if got := (commandOptions{DiffContext: 0, DiffContextSet: true}).diffContext(); got != 0 {
		t.Fatalf("-U0 context = %d", got)
	}
	if got := (commandOptions{DiffContext: -1, DiffContextSet: true}).diffContext(); got != -1 {
		t.Fatalf("-U-1 context = %d", got)
	}
}
