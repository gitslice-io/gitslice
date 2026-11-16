package cli_test

import (
	"strings"
	"testing"
)

// TestMergeRulesResolveSyncConflicts covers fb_3fc4 (3): .gitslice/merge.yaml
// resolves what a line merge cannot, both when the server restacks a
// dependency tree (gs create) and when the client merges (gs cs create).
func TestMergeRulesResolveSyncConflicts(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	theirs := t.TempDir()
	loginTestCLI(t, ts, home, theirs)
	runCLI(t, home, theirs, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, theirs, "acme/payment/.gitslice/merge.yaml",
		"version: 1\nrules:\n  - paths: [\"**/go.sum\"]\n    merge: union\n  - paths: [\"**/*.lock\"]\n    merge: newest\n")
	writeWorkspaceFile(t, theirs, "acme/payment/go.sum", "a v1 h1\nb v1 h1\n")
	writeWorkspaceFile(t, theirs, "acme/payment/deps.lock", "version 1\n")
	runCLI(t, home, theirs, "create", "--all", "--message", "rules", "--submit")

	for _, mode := range []string{"server", "client"} {
		mine := t.TempDir()
		runCLI(t, home, mine, "workspace", "init", "acme/payment")
		writeWorkspaceFile(t, mine, "acme/payment/go.sum", "a v1 h1\nb v1 h1\nmine-"+mode+" v1 h1\n")
		writeWorkspaceFile(t, mine, "acme/payment/deps.lock", "version mine-"+mode+"\n")
		if mode == "server" {
			runCLI(t, home, mine, "create", "--all", "--message", "mine "+mode)
		} else {
			runCLI(t, home, mine, "cs", "create", "--title", "mine "+mode)
		}

		runCLI(t, home, theirs, "sync")
		writeWorkspaceFile(t, theirs, "acme/payment/go.sum", "a v1 h1\nb v1 h1\ntheirs-"+mode+" v1 h1\n")
		writeWorkspaceFile(t, theirs, "acme/payment/deps.lock", "version theirs-"+mode+"\n")
		runCLI(t, home, theirs, "create", "--all", "--message", "theirs "+mode, "--submit")

		out := runCLI(t, home, mine, "sync")
		if strings.Contains(out, "conflicts:") {
			t.Fatalf("%s: sync left conflicts:\n%s", mode, out)
		}
		assertWorkspaceFile(t, mine, "acme/payment/go.sum", "a v1 h1\nb v1 h1\ntheirs-"+mode+" v1 h1\nmine-"+mode+" v1 h1\n")
		assertWorkspaceFile(t, mine, "acme/payment/deps.lock", "version theirs-"+mode+"\n")
		runCLI(t, home, theirs, "sync")
	}
}
