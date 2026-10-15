package diffutil

import (
	"fmt"
	"strings"
	"testing"
)

func TestMerge3(t *testing.T) {
	lines := func(ls ...string) string {
		if len(ls) == 0 {
			return ""
		}
		return strings.Join(ls, "\n") + "\n"
	}
	base := lines("a", "b", "c", "d", "e", "f", "g")
	for _, tc := range []struct {
		name          string
		ours, theirs  string
		want          string
		wantClean     bool
		baseOverrides *string
	}{
		{name: "different lines", ours: lines("A", "b", "c", "d", "e", "f", "g"), theirs: lines("a", "b", "c", "d", "e", "f", "G"), want: lines("A", "b", "c", "d", "e", "f", "G"), wantClean: true},
		{name: "only ours", ours: lines("a", "b", "C", "d", "e", "f", "g"), theirs: base, want: lines("a", "b", "C", "d", "e", "f", "g"), wantClean: true},
		{name: "only theirs", ours: base, theirs: lines("a", "b", "c", "D", "e", "f", "g"), want: lines("a", "b", "c", "D", "e", "f", "g"), wantClean: true},
		{name: "same change", ours: lines("a", "b", "X", "d", "e", "f", "g"), theirs: lines("a", "b", "X", "d", "e", "f", "g", "h"), want: lines("a", "b", "X", "d", "e", "f", "g", "h"), wantClean: true},
		{name: "insert and delete apart", ours: lines("a", "new", "b", "c", "d", "e", "f", "g"), theirs: lines("a", "b", "c", "d", "e", "g"), want: lines("a", "new", "b", "c", "d", "e", "g"), wantClean: true},
		{name: "same line differently", ours: lines("a", "b", "c", "OURS", "e", "f", "g"), theirs: lines("a", "b", "c", "THEIRS", "e", "f", "g"),
			want: lines("a", "b", "c", "<<<<<<< ours", "OURS", "=======", "THEIRS", ">>>>>>> theirs", "e", "f", "g")},
		{name: "adjacent lines", ours: lines("a", "b", "C", "d", "e", "f", "g"), theirs: lines("a", "b", "c", "D", "e", "f", "g"),
			want: lines("a", "b", "<<<<<<< ours", "C", "d", "=======", "c", "D", ">>>>>>> theirs", "e", "f", "g")},
		{name: "insertions at the same point", ours: lines("a", "b", "c", "d", "x", "e", "f", "g"), theirs: lines("a", "b", "c", "d", "y", "e", "f", "g"),
			want: lines("a", "b", "c", "d", "<<<<<<< ours", "x", "=======", "y", ">>>>>>> theirs", "e", "f", "g")},
		{name: "delete what the other changed", ours: lines("a", "b", "c", "e", "f", "g"), theirs: lines("a", "b", "c", "D", "e", "f", "g"),
			want: lines("a", "b", "c", "<<<<<<< ours", "=======", "D", ">>>>>>> theirs", "e", "f", "g")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, clean := Merge3([]byte(base), []byte(tc.ours), []byte(tc.theirs), "ours", "theirs")
			if string(got) != tc.want || clean != tc.wantClean {
				t.Fatalf("Merge3 = %q (clean %v)\nwant    %q (clean %v)", got, clean, tc.want, tc.wantClean)
			}
		})
	}

	// No trailing newline on either side; markers still start their own line.
	got, clean := Merge3([]byte("a\nb"), []byte("a\nours"), []byte("a\ntheirs"), "ours", "theirs")
	if clean || string(got) != "a\n<<<<<<< ours\nours\n=======\ntheirs\n>>>>>>> theirs\n" {
		t.Fatalf("no trailing newline: %q", got)
	}
	// Both added a file that did not exist.
	if got, clean := Merge3(nil, []byte("x\n"), []byte("y\n"), "ours", "theirs"); clean || string(got) != "<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n" {
		t.Fatalf("empty base: %q", got)
	}

	// A large file with edits far apart merges cleanly and stays exact.
	var big []string
	for i := range 20000 {
		big = append(big, fmt.Sprintf("line %d", i))
	}
	edit := func(i int, s string) string {
		copyLines := append([]string(nil), big...)
		copyLines[i] = s
		return lines(copyLines...)
	}
	got, clean = Merge3([]byte(lines(big...)), []byte(edit(10, "ours")), []byte(edit(19000, "theirs")), "ours", "theirs")
	want := append([]string(nil), big...)
	want[10], want[19000] = "ours", "theirs"
	if !clean || string(got) != lines(want...) {
		t.Fatalf("large file merge was not clean or not exact (clean %v)", clean)
	}
}
