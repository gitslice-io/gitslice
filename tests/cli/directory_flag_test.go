package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDirectoryFlagAndInitIntoDir covers fb_d155: gs init <slice> <dir>
// checks out into a new directory, says where the files are, and gs -C <dir>
// runs a command as if started there.
func TestDirectoryFlagAndInitIntoDir(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	parent := t.TempDir()
	loginTestCLI(t, ts, home, parent)

	out := runCLI(t, home, parent, "init", "acme/payment", "pay")
	if !strings.Contains(out, "initialized workspace for acme:payment") || !strings.Contains(out, "files are under ./acme/payment") {
		t.Fatalf("gs init <slice> <dir> =\n%s", out)
	}
	workspace := filepath.Join(parent, "pay")
	if _, err := os.Stat(filepath.Join(workspace, ".gs")); err != nil {
		t.Fatalf("no workspace in %s: %v", workspace, err)
	}

	writeWorkspaceFile(t, workspace, "dirflag.go", "package payment\nconst DirFlag = 1\n")
	status := runCLI(t, home, parent, "-C", "pay", "status")
	if !strings.Contains(status, "dirflag.go") {
		t.Fatalf("gs -C pay status =\n%s", status)
	}
	if status2 := runCLI(t, home, parent, "-Cpay", "status"); status2 != status {
		t.Fatalf("gs -Cpay status differs:\n%s\nvs\n%s", status2, status)
	}

	// A non-empty directory is refused, and so is a missing -C directory.
	_, stderr := runCLIFails(t, home, parent, "init", "acme/payment", "pay")
	if !strings.Contains(stderr, "is not empty") {
		t.Fatalf("init into a non-empty dir: %s", stderr)
	}
	_, stderr = runCLIFails(t, home, parent, "-C", "missing", "status")
	if !strings.Contains(stderr, "no such directory") {
		t.Fatalf("-C missing: %s", stderr)
	}
}
