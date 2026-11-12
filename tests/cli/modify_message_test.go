package cli_test

import (
	"strings"
	"testing"
)

// TestModifyMessageRetitles checks that gs modify --message gives the
// changeset its new title along with the new patchset.
func TestModifyMessageRetitles(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspace, "acme/payment/draft.go", "package payment\nconst Draft = 1\n")
	runCLI(t, home, workspace, "create", "--message", "first title", "--all")
	writeWorkspaceFile(t, workspace, "acme/payment/draft.go", "package payment\nconst Draft = 2\n")
	runCLI(t, home, workspace, "modify", "--all", "--message", "second title")

	show := runCLI(t, home, workspace, "cs", "show")
	if !strings.Contains(show, "second title") || strings.Contains(show, "first title") {
		t.Fatalf("cs show after modify --message:\n%s", show)
	}
}
