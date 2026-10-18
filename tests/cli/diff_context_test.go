package cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestDiffShowsContextAroundChangesOnly: gs diff and gs cs diff print the usual
// hunks with three lines of context, -U changes that, and --json keeps the
// whole file (the web viewer folds it itself).
func TestDiffShowsContextAroundChangesOnly(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	writeWorkspaceFile(t, workspace, "acme/payment/long.txt", b.String())
	runCLI(t, home, workspace, "create", "--message", "a long file", "--all", "--submit")
	runCLI(t, home, workspace, "sync")

	edited := strings.Replace(b.String(), "line 20\n", "line twenty\n", 1)
	writeWorkspaceFile(t, workspace, "acme/payment/long.txt", edited)

	diff := runCLI(t, home, workspace, "diff")
	if !strings.Contains(diff, "@@ -17,7 +17,7 @@\n line 17\n line 18\n line 19\n-line 20\n+line twenty\n line 21\n line 22\n line 23\n") {
		t.Fatalf("gs diff of a one-line edit:\n%s", diff)
	}
	if strings.Contains(diff, " line 1\n") || strings.Contains(diff, " line 40\n") {
		t.Fatalf("gs diff shows lines far from the change:\n%s", diff)
	}
	if zero := runCLI(t, home, workspace, "diff", "-U0"); !strings.Contains(zero, "@@ -20,1 +20,1 @@\n-line 20\n+line twenty\n") || strings.Contains(zero, " line 19") {
		t.Fatalf("gs diff -U0:\n%s", zero)
	}
	if whole := runCLI(t, home, workspace, "diff", "--unified=-1"); !strings.Contains(whole, "@@ -1,40 +1,40 @@") || !strings.Contains(whole, " line 1\n") || !strings.Contains(whole, " line 40\n") {
		t.Fatalf("gs diff --unified=-1 should show the whole file:\n%s", whole)
	}
	var out struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "diff", "--json")), &out); err != nil || !strings.Contains(out.Diff, "@@ -1,40 +1,40 @@") {
		t.Fatalf("gs diff --json should keep the whole-file hunk: %v\n%s", err, out.Diff)
	}

	// The same through a changeset's server-side diff.
	runCLI(t, home, workspace, "create", "--message", "edit line 20", "--all")
	if csDiff := runCLI(t, home, workspace, "cs", "diff"); !strings.Contains(csDiff, "@@ -17,7 +17,7 @@\n") || strings.Contains(csDiff, " line 40\n") {
		t.Fatalf("gs cs diff:\n%s", csDiff)
	}
	if csZero := runCLI(t, home, workspace, "cs", "diff", "-U", "1"); !strings.Contains(csZero, "@@ -19,3 +19,3 @@\n line 19\n-line 20\n+line twenty\n line 21\n") {
		t.Fatalf("gs cs diff -U 1:\n%s", csZero)
	}
}
