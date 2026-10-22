package cli_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBlameFollowsLinesAndMoves lands four changes to a file, the third a
// move, and checks gs blame credits each line to the change that last
// touched it, with the changeset that landed it.
func TestBlameFollowsLinesAndMoves(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	land := func(message string) {
		t.Helper()
		if out := runCLI(t, home, workspace, "create", "--all", "--message", message, "--submit"); !strings.Contains(out, "submitted ") {
			t.Fatalf("%s:\n%s", message, out)
		}
	}
	writeWorkspaceFile(t, workspace, "blame.go", "package payment\nconst A = 1\nconst B = 1\n")
	land("add blame.go")
	writeWorkspaceFile(t, workspace, "blame.go", "package payment\nconst A = 2\nconst B = 1\nconst C = 2\n")
	land("change A, add C")
	data, err := os.ReadFile(filepath.Join(workspace, "blame.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workspace, "blame.go")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, workspace, "renamed.go", string(data))
	land("move to renamed.go")
	writeWorkspaceFile(t, workspace, "renamed.go", "package payment // moved\nconst A = 2\nconst B = 1\nconst C = 2\n")
	land("comment the package")

	var blame struct {
		Path      string `json:"path"`
		LineCount int    `json:"line_count"`
		Truncated bool   `json:"truncated"`
		Ranges    []struct {
			StartLine int    `json:"start_line"`
			LineCount int    `json:"line_count"`
			CommitID  string `json:"commit_id"`
		} `json:"ranges"`
		Commits []struct {
			ID           string `json:"id"`
			Summary      string `json:"summary"`
			ChangesetID  string `json:"changeset_id"`
			ChangesetURL string `json:"changeset_url"`
		} `json:"commits"`
	}
	out := runCLI(t, home, workspace, "blame", "renamed.go", "--json")
	if err := json.Unmarshal([]byte(out), &blame); err != nil {
		t.Fatalf("blame --json: %v\n%s", err, out)
	}
	summaries := map[string]string{}
	for _, c := range blame.Commits {
		summaries[c.ID] = c.Summary
		if !strings.HasPrefix(c.ChangesetID, "cs_") || !strings.Contains(c.ChangesetURL, "/cs/") {
			t.Fatalf("commit without its changeset: %+v", c)
		}
	}
	perLine := []string{}
	for _, rg := range blame.Ranges {
		for i := 0; i < rg.LineCount; i++ {
			perLine = append(perLine, summaries[rg.CommitID])
		}
	}
	want := []string{"comment the package", "change A, add C", "add blame.go", "change A, add C"}
	if blame.Path != "/acme/payment/renamed.go" || blame.LineCount != 4 || blame.Truncated || strings.Join(perLine, "|") != strings.Join(want, "|") {
		t.Fatalf("blame = %+v\nper line %q, want %q", blame, perLine, want)
	}

	// The blame was stored as a snapshot. After one more change, the next
	// blame starts from it, and still credits every line correctly.
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	countSnapshots := func() int {
		var n int
		if err := db.QueryRow(`select count(*) from blame_snapshots`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countSnapshots(); n != 1 {
		t.Fatalf("snapshots after the first blame = %d, want 1", n)
	}
	writeWorkspaceFile(t, workspace, "renamed.go", "package payment // moved\nconst A = 2\nconst B = 3\nconst C = 2\n")
	land("change B")
	perLineSummaries := func() []string {
		t.Helper()
		var again struct {
			Ranges []struct {
				LineCount int    `json:"line_count"`
				CommitID  string `json:"commit_id"`
			} `json:"ranges"`
			Commits []struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
			} `json:"commits"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "blame", "renamed.go", "--json")), &again); err != nil {
			t.Fatal(err)
		}
		names := map[string]string{}
		for _, c := range again.Commits {
			names[c.ID] = c.Summary
		}
		var out []string
		for _, rg := range again.Ranges {
			for i := 0; i < rg.LineCount; i++ {
				out = append(out, names[rg.CommitID])
			}
		}
		return out
	}
	want = []string{"comment the package", "change A, add C", "change B", "change A, add C"}
	if got := perLineSummaries(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("blame from the snapshot: %q, want %q", got, want)
	}
	if n := countSnapshots(); n != 2 {
		t.Fatalf("snapshots after the second blame = %d, want 2", n)
	}
	// Without snapshots, the full walk gives the same answer.
	if _, err := db.Exec(`delete from blame_snapshots`); err != nil {
		t.Fatal(err)
	}
	if got := perLineSummaries(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("blame without snapshots: %q, want %q", got, want)
	}

	text := runCLI(t, home, workspace, "blame", "renamed.go", "--lines", "2,3")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " 2) const A = 2") || !strings.HasSuffix(lines[1], " 3) const B = 3") {
		t.Fatalf("blame text:\n%s", text)
	}
	if _, stderr := runCLIFails(t, home, workspace, "blame", "missing.go"); !strings.Contains(stderr, "no file at /acme/payment/missing.go") {
		t.Fatalf("missing file: %s", stderr)
	}
}

// A move is followed even when the commit that made it also changed another
// file: the search looks at each changed path (in parallel), and must pick
// the one the file came from, not the commit that moved it, and not a file
// that merely changed in the same commit.
func TestBlameFollowsAMoveAlongsideOtherChanges(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	land := func(message string) {
		t.Helper()
		if out := runCLI(t, home, workspace, "create", "--all", "--message", message, "--submit"); !strings.Contains(out, "submitted ") {
			t.Fatalf("%s:\n%s", message, out)
		}
	}
	original := "package payment\n\nconst Rate = 3\nconst Fee = 2\nconst Cap = 100\n"
	writeWorkspaceFile(t, workspace, "old/pricing.go", original)
	writeWorkspaceFile(t, workspace, "old/other.go", "package payment\n\nconst Other = 1\n")
	land("add pricing")
	if err := os.Remove(filepath.Join(workspace, "old", "pricing.go")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, workspace, "new/pricing.go", original)
	writeWorkspaceFile(t, workspace, "old/other.go", "package payment\n\nconst Other = 2\n")
	land("move pricing, change other")

	// Following the move needs the move in the file's indexed history, which
	// follows the landing by a moment; a blame in between is stored too, so
	// the snapshots are cleared before the check.
	want := strings.TrimSuffix(strings.Repeat("add pricing|", 5), "|")
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := db.Exec(`delete from blame_snapshots`); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(blameSummaries(t, home, workspace, "new/pricing.go"), "|")
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("blame walked = %q, want %q", got, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := strings.Join(blameSummaries(t, home, workspace, "new/pricing.go"), "|"); got != want {
		t.Fatalf("blame from the snapshot = %q, want %q", got, want)
	}
}

// The first blame of a file made in a commit that created dozens of others
// looks at each of them for a move, none of which is one. It still credits
// the lines right, from a walk and from the snapshot it stored.
func TestBlameOfAFileCreatedAmongManyFiles(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	land := func(message string) {
		t.Helper()
		if out := runCLI(t, home, workspace, "create", "--all", "--message", message, "--submit"); !strings.Contains(out, "submitted ") {
			t.Fatalf("%s:\n%s", message, out)
		}
	}
	writeWorkspaceFile(t, workspace, "target.go", "package payment\n\nconst A = 1\nconst B = 2\n")
	for i := 0; i < 40; i++ {
		writeWorkspaceFile(t, workspace, fmt.Sprintf("bulk/f%02d.go", i), fmt.Sprintf("package bulk\n\nconst N%d = %d\n", i, i))
	}
	land("create target among many files")
	writeWorkspaceFile(t, workspace, "target.go", "package payment\n\nconst A = 1\nconst B = 3\n")
	land("change B")

	want := "create target among many files|create target among many files|create target among many files|change B"
	for _, round := range []string{"walked", "from the snapshot"} {
		if got := strings.Join(blameSummaries(t, home, workspace, "target.go"), "|"); got != want {
			t.Fatalf("blame %s = %q, want %q", round, got, want)
		}
	}
}

// blameSummaries is the subject of the commit credited with each line.
func blameSummaries(t *testing.T, home, workspace, file string) []string {
	t.Helper()
	var out struct {
		Ranges []struct {
			LineCount int    `json:"line_count"`
			CommitID  string `json:"commit_id"`
		} `json:"ranges"`
		Commits []struct {
			ID      string `json:"id"`
			Summary string `json:"summary"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "blame", file, "--json")), &out); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, c := range out.Commits {
		names[c.ID] = c.Summary
	}
	var lines []string
	for _, rg := range out.Ranges {
		for i := 0; i < rg.LineCount; i++ {
			lines = append(lines, names[rg.CommitID])
		}
	}
	return lines
}

// A blame whose history does not reach the file's start can only guess for the
// older lines (it credits them to the oldest commit it read, and says so with
// truncated). Happens right after a landing, while history is still being
// indexed. A guess must not be stored: it would be reused, and wrong, for good.
func TestBlameDoesNotStoreAGuess(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	land := func(message string) {
		t.Helper()
		if out := runCLI(t, home, workspace, "create", "--all", "--message", message, "--submit"); !strings.Contains(out, "submitted ") {
			t.Fatalf("%s:\n%s", message, out)
		}
	}
	content := "package payment\n\nconst Rate = 3\nconst Fee = 2\n"
	writeWorkspaceFile(t, workspace, "old/pricing.go", content)
	land("add pricing")
	if err := os.Remove(filepath.Join(workspace, "old", "pricing.go")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, workspace, "new/pricing.go", content)
	land("move pricing")

	blame := func() (truncated bool, summaries string) {
		t.Helper()
		var out struct {
			Truncated bool `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "blame", "new/pricing.go", "--json")), &out); err != nil {
			t.Fatal(err)
		}
		return out.Truncated, strings.Join(blameSummaries(t, home, workspace, "new/pricing.go"), "|")
	}
	stored := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`select count(*) from blame_snapshots`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// History as it is just after a landing: the move is not indexed yet.
	// Waiting for any indexing already under way first, so it cannot add rows
	// after they are cleared.
	time.Sleep(time.Second)
	if _, err := db.Exec(`delete from commit_entity_changes`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from blame_snapshots`); err != nil {
		t.Fatal(err)
	}
	if truncated, got := blame(); !truncated {
		t.Fatalf("the blame without the move in history was not marked truncated: %q", got)
	}
	if n := stored(); n != 0 {
		t.Fatalf("a guessed blame was stored as %d snapshot(s)", n)
	}
	if truncated, _ := blame(); !truncated {
		t.Fatal("a second blame of the same file found a stored guess instead of walking again")
	}
}

// The server blames the files a landing changed by itself, so the first person
// to open one finds a snapshot already stored: for a file the landing created,
// and for one it changed that nobody had blamed.
func TestServerWarmsTheBlamesOfWhatALandingChanged(t *testing.T) {
	ts := startTestServerWith(t, true)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	land := func(message string) {
		t.Helper()
		if out := runCLI(t, home, workspace, "create", "--all", "--message", message, "--submit"); !strings.Contains(out, "submitted ") {
			t.Fatalf("%s:\n%s", message, out)
		}
	}
	// Let the warmer take its starting head before anything lands.
	time.Sleep(4 * time.Second)
	writeWorkspaceFile(t, workspace, "calc.go", "package payment\n\nconst A = 1\nconst B = 2\n")
	land("add calc")
	writeWorkspaceFile(t, workspace, "calc.go", "package payment\n\nconst A = 1\nconst B = 3\n")
	writeWorkspaceFile(t, workspace, "extra.go", "package payment\n\nconst X = 1\n")
	land("change calc, add extra")

	snapshotsFor := func() map[string]bool {
		t.Helper()
		rows, err := db.Query(`select path from blame_snapshots`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			out[p] = true
		}
		return out
	}
	// No blame has been asked for: the warmer stores them.
	deadline := time.Now().Add(40 * time.Second)
	for {
		got := snapshotsFor()
		if got["/acme/payment/calc.go"] && got["/acme/payment/extra.go"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the warmer stored %v, want snapshots of calc.go and extra.go", got)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// The blame it stored is the right one, and reading it stores nothing new.
	before := len(snapshotsFor())
	want := "add calc|add calc|add calc|change calc, add extra"
	if got := strings.Join(blameSummaries(t, home, workspace, "calc.go"), "|"); got != want {
		t.Fatalf("blame of a warmed file = %q, want %q", got, want)
	}
	if after := len(snapshotsFor()); after != before {
		t.Fatalf("blaming a warmed file stored %d more snapshot(s)", after-before)
	}
}
