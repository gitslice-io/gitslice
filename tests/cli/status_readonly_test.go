package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestStatusWithoutWriteAccess covers fb_d155: in a checkout of a slice the
// caller can read but not write, gs status lists the workspace's own edits
// (the server refuses to validate them) instead of failing, and --local does
// the same without asking.
func TestStatusWithoutWriteAccess(t *testing.T) {
	ts := startTestServer(t)
	homes := map[string]string{}
	for _, name := range []string{"owner-agent", "reader-agent"} {
		token, _, subject := ts.provisionAccount(t, name, name)
		homes[name] = t.TempDir()
		writeCLIAuthConfig(t, homes[name], ts.addr, token, subject)
	}
	owner, reader := homes["owner-agent"], homes["reader-agent"]
	ownerWS := t.TempDir()
	runCLI(t, owner, ownerWS, "workspace", "init", "owner-agent/home")
	writeWorkspaceFile(t, ownerWS, "owner-agent/docs/readme.md", "# docs\n")
	runCLI(t, owner, ownerWS, "create", "--all", "--message", "docs", "--submit")
	runCLI(t, owner, t.TempDir(), "slice", "create", "owner-agent/docs", "--visibility", "public")

	readerWS := t.TempDir()
	runCLI(t, reader, readerWS, "init", "owner-agent/docs")
	writeWorkspaceFile(t, readerWS, "owner-agent/docs/readme.md", "# docs\nmore\n")
	out := runCLI(t, reader, readerWS, "status")
	if !strings.Contains(out, "readme.md") || !strings.Contains(out, "local: showing this workspace's own edits") {
		t.Fatalf("gs status without write access =\n%s", out)
	}
	out = runCLI(t, reader, readerWS, "status", "--local")
	if !strings.Contains(out, "readme.md") || !strings.Contains(out, "--local") {
		t.Fatalf("gs status --local =\n%s", out)
	}
	// --relative names paths as a shell in the current directory would.
	rel := runCLI(t, reader, filepath.Join(readerWS, "owner-agent", "docs"), "status", "--local", "--relative")
	if !strings.Contains(rel, "modified  readme.md") || strings.Contains(rel, "/owner-agent/docs/readme.md") {
		t.Fatalf("gs status --relative =\n%s", rel)
	}
	if js := runCLI(t, reader, readerWS, "status", "--json"); !strings.Contains(js, `"local_only": "refused"`) {
		t.Fatalf("gs status --json =\n%s", js)
	}
}
