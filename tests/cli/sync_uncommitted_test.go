package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncMergesUncommittedEdits covers fb_d155: gs sync with edits and no
// changeset merges the new head into them when it merges cleanly, after a
// backup, and refuses (changing nothing) when the head conflicts with them.
func TestSyncMergesUncommittedEdits(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	mine := t.TempDir()
	theirs := t.TempDir()
	loginTestCLI(t, ts, home, mine)
	runCLI(t, home, theirs, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, theirs, "acme/payment/shared.txt", "one\ntwo\nthree\nfour\nfive\n")
	runCLI(t, home, theirs, "create", "--all", "--message", "shared", "--submit")
	runCLI(t, home, mine, "workspace", "init", "acme/payment")

	// My uncommitted edit at the top; their landed edit at the bottom.
	writeWorkspaceFile(t, mine, "acme/payment/shared.txt", "ONE\ntwo\nthree\nfour\nfive\n")
	writeWorkspaceFile(t, mine, "acme/payment/mine.txt", "only mine\n")
	runCLI(t, home, theirs, "sync")
	writeWorkspaceFile(t, theirs, "acme/payment/shared.txt", "one\ntwo\nthree\nfour\nFIVE\n")
	writeWorkspaceFile(t, theirs, "acme/payment/theirs.txt", "theirs\n")
	runCLI(t, home, theirs, "create", "--all", "--message", "theirs", "--submit")

	out := runCLI(t, home, mine, "sync")
	if !strings.Contains(out, "your uncommitted edits were copied to .gs/sync-backup/") {
		t.Fatalf("sync with uncommitted edits =\n%s", out)
	}
	assertWorkspaceFile(t, mine, "acme/payment/shared.txt", "ONE\ntwo\nthree\nfour\nFIVE\n")
	assertWorkspaceFile(t, mine, "acme/payment/mine.txt", "only mine\n")
	assertWorkspaceFile(t, mine, "acme/payment/theirs.txt", "theirs\n")
	backups, _ := filepath.Glob(filepath.Join(mine, ".gs", "sync-backup", "*", "acme", "payment", "shared.txt"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if data, _ := os.ReadFile(backups[0]); string(data) != "ONE\ntwo\nthree\nfour\nfive\n" {
		t.Fatalf("backup of shared.txt = %q", data)
	}

	// A head that changes the same line as an uncommitted edit: refused,
	// nothing touched.
	writeWorkspaceFile(t, mine, "acme/payment/shared.txt", "ONE!\ntwo\nthree\nfour\nFIVE\n")
	runCLI(t, home, theirs, "sync")
	writeWorkspaceFile(t, theirs, "acme/payment/shared.txt", "one?\ntwo\nthree\nfour\nFIVE\n")
	runCLI(t, home, theirs, "create", "--all", "--message", "conflicting", "--submit")
	_, stderr := runCLIFails(t, home, mine, "sync")
	if !strings.Contains(stderr, "conflicts with 1 of your uncommitted edits") || !strings.Contains(stderr, "nothing was changed") {
		t.Fatalf("conflicting sync: %s", stderr)
	}
	assertWorkspaceFile(t, mine, "acme/payment/shared.txt", "ONE!\ntwo\nthree\nfour\nFIVE\n")
}
