package diffutil

import (
	"strings"
	"testing"
)

// Files without a trailing newline must still produce a well-formed unified diff
// where every +/-/context entry is on its own line (no joined lines).
func TestUnifiedFileDiffNewlineTerminated(t *testing.T) {
	old := File{Path: "/x/notes.txt", Exists: true, Data: []byte("line one\nline two\nline three")}
	neu := File{Path: "/x/notes.txt", Exists: true, Data: []byte("line one\nline two CHANGED\nline three\nline four added")}

	out := UnifiedFileDiff(old, neu)
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("diff should end with a newline:\n%q", out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		// A body line carries a single +/-/space prefix; it must not contain a
		// second diff entry joined onto it.
		if strings.Contains(trimmed, "+") && strings.HasPrefix(line, "-") {
			t.Fatalf("found a joined diff line: %q", line)
		}
	}
	if strings.Contains(out, "line three+line two CHANGED") {
		t.Fatalf("deletion and addition were joined on one line:\n%q", out)
	}
}
