package diffutil

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func lcsLength(a, b []string) int {
	n := 0
	for _, line := range lcsDiff(a, b) {
		if strings.HasPrefix(line, " ") {
			n++
		}
	}
	return n
}

func checkMatch(t *testing.T, oldLines, newLines []string, match []int) int {
	t.Helper()
	last, matched := -1, 0
	for j, i := range match {
		if i < 0 {
			continue
		}
		if i <= last || oldLines[i] != newLines[j] {
			t.Fatalf("bad match %d->%d (last %d): %q vs %q", j, i, last, newLines[j], oldLines[i])
		}
		last = i
		matched++
	}
	return matched
}

func TestMatchLinesFindsTheLongestCommonSubsequence(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 500; round++ {
		alphabet := 2 + rng.Intn(6)
		gen := func(n int) []string {
			out := make([]string, n)
			for i := range out {
				out[i] = strconv.Itoa(rng.Intn(alphabet))
			}
			return out
		}
		oldLines, newLines := gen(rng.Intn(40)), gen(rng.Intn(40))
		match := MatchLines(oldLines, newLines, 0)
		if got, want := checkMatch(t, oldLines, newLines, match), lcsLength(oldLines, newLines); got != want {
			t.Fatalf("round %d: matched %d lines, the LCS has %d\nold %v\nnew %v", round, got, want, oldLines, newLines)
		}
	}
}

func TestMatchLinesTypicalEdit(t *testing.T) {
	oldLines := []string{"package a\n", "\n", "func f() {\n", "\treturn 1\n", "}\n"}
	newLines := []string{"package a\n", "\n", "// f returns 2.\n", "func f() {\n", "\treturn 2\n", "}\n"}
	match := MatchLines(oldLines, newLines, 0)
	want := []int{0, 1, -1, 2, -1, 4}
	for j := range want {
		if match[j] != want[j] {
			t.Fatalf("match = %v, want %v", match, want)
		}
	}
}

func TestMatchLinesBoundsHugeRewrites(t *testing.T) {
	var oldLines, newLines []string
	for i := 0; i < 3000; i++ {
		oldLines = append(oldLines, "old "+strconv.Itoa(i)+"\n")
		newLines = append(newLines, "new "+strconv.Itoa(i)+"\n")
	}
	oldLines = append(oldLines, "same\n")
	newLines = append(newLines, "same\n")
	// More edits than allowed: the middle counts as rewritten, the common
	// suffix still matches.
	match := MatchLines(oldLines, newLines, 100)
	if match[len(match)-1] != len(oldLines)-1 {
		t.Fatalf("suffix not matched")
	}
	for j := 0; j < 3000; j++ {
		if match[j] != -1 {
			t.Fatalf("line %d matched %d", j, match[j])
		}
	}
	// A large file with a small edit is quick and exact.
	big := make([]string, 50000)
	for i := range big {
		big[i] = "line " + strconv.Itoa(i) + "\n"
	}
	edited := append(append(append([]string{}, big[:25000]...), "inserted\n"), big[25000:]...)
	match = MatchLines(big, edited, 0)
	if checkMatch(t, big, edited, match) != len(big) || match[25000] != -1 {
		t.Fatal("small edit in a big file")
	}
}
