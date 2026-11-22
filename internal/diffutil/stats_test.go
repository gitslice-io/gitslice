package diffutil

import "testing"

func TestLineStats(t *testing.T) {
	cases := []struct {
		name           string
		old, new       string
		added, removed int
		binary         bool
	}{
		{"one line edited", "a\nb\nc\n", "a\nB\nc\n", 1, 1, false},
		{"lines added", "a\nc\n", "a\nb1\nb2\nc\n", 2, 0, false},
		{"new file", "", "x\ny\n", 2, 0, false},
		{"deleted file", "x\ny\nz\n", "", 0, 3, false},
		{"no trailing newline", "a\nb", "a\nb\n", 1, 1, false},
		{"unchanged", "a\n", "a\n", 0, 0, false},
		{"moved line", "a\nb\nc\n", "b\nc\na\n", 1, 1, false},
		{"binary", "a\x00b", "a\x00c", 0, 0, true},
	}
	for _, tc := range cases {
		added, removed, binary := LineStats([]byte(tc.old), []byte(tc.new))
		if added != tc.added || removed != tc.removed || binary != tc.binary {
			t.Errorf("%s: LineStats = +%d -%d binary=%v, want +%d -%d binary=%v", tc.name, added, removed, binary, tc.added, tc.removed, tc.binary)
		}
	}
}

func TestLineStatsTooLargeCountsTheMiddle(t *testing.T) {
	saved := maxLCSCells
	maxLCSCells = 4
	defer func() { maxLCSCells = saved }()
	added, removed, _ := LineStats([]byte("k\na\nb\nc\nk\n"), []byte("k\nx\ny\nz\nk\n"))
	if added != 3 || removed != 3 {
		t.Fatalf("LineStats = +%d -%d, want +3 -3", added, removed)
	}
}

func TestLineStatsLargeFileWithSmallScatteredEdits(t *testing.T) {
	// Simulates a generated file (~2,500 lines) with edits near top and bottom
	// so len(oldCore)*len(newCore) >> maxLCSCells (1,000,000), while the real
	// edit distance is small.
	const total = 2500
	var oldBuf, newBuf []byte
	for i := 0; i < total; i++ {
		line := "line_" + string(rune('a'+(i%26))) + "\n"
		if i == 5 {
			oldBuf = append(oldBuf, "old_import\n"...)
			newBuf = append(newBuf, "new_import\n"...)
			continue
		}
		if i == total-10 {
			oldBuf = append(oldBuf, "old_tail\n"...)
			newBuf = append(newBuf, "new_tail_1\nnew_tail_2\nnew_tail_3\n"...)
			continue
		}
		oldBuf = append(oldBuf, line...)
		newBuf = append(newBuf, line...)
	}
	added, removed, binary := LineStats(oldBuf, newBuf)
	if binary || added != 4 || removed != 2 {
		t.Fatalf("LineStats(2500 lines, scattered edits) = +%d -%d binary=%v, want +4 -2 binary=false", added, removed, binary)
	}
}
