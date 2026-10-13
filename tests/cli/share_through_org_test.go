package cli_test

import (
	"strings"
	"testing"
)

// TestTwoAgentsShareThroughAnOrganization follows the "working with another
// agent" steps in llms.txt and the gs-cli skill: one agent creates an
// organization and invites the other, who accepts and lands a change in the
// organization's home slice that the first one then sees.
func TestTwoAgentsShareThroughAnOrganization(t *testing.T) {
	ts := startTestServer(t)
	aliceToken, _, aliceSubject := ts.provisionAccount(t, "alice-agent", "alice-agent")
	bobToken, _, bobSubject := ts.provisionAccount(t, "bob-agent", "bob-agent")
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	writeCLIAuthConfig(t, aliceHome, ts.addr, aliceToken, aliceSubject)
	writeCLIAuthConfig(t, bobHome, ts.addr, bobToken, bobSubject)

	runCLI(t, aliceHome, t.TempDir(), "account", "create-org", "team-x")
	runCLI(t, aliceHome, t.TempDir(), "account", "invite", "team-x", "bob-agent", "--role", "writer")
	// A slice for paths with no files yet is refused, with a pointer to home.
	if _, stderr := runCLIFails(t, aliceHome, t.TempDir(), "slice", "create", "team-x/project"); !strings.Contains(stderr, "gs init team-x/home") {
		t.Fatalf("slice for an empty path: %s", stderr)
	}

	// Before accepting, bob cannot work in it.
	if _, err := runCLIResult(bobHome, t.TempDir(), "workspace", "init", "team-x/home"); err == nil {
		t.Fatal("bob could use the organization's slice before accepting")
	}
	if out := runCLI(t, bobHome, t.TempDir(), "account", "invitations"); !strings.Contains(out, "team-x") {
		t.Fatalf("bob's invitations:\n%s", out)
	}
	runCLI(t, bobHome, t.TempDir(), "account", "accept", "team-x")

	// Files sit at their full path in a workspace: team-x/project/...
	bobWS := t.TempDir()
	runCLI(t, bobHome, bobWS, "workspace", "init", "team-x/home")
	writeWorkspaceFile(t, bobWS, "team-x/project/plan.md", "# shared plan\n")
	if out := runCLI(t, bobHome, bobWS, "create", "--all", "--message", "add the shared plan", "--submit"); !strings.Contains(out, "submitted ") {
		t.Fatalf("bob's change:\n%s", out)
	}

	aliceWS := t.TempDir()
	runCLI(t, aliceHome, aliceWS, "workspace", "init", "team-x/home")
	assertWorkspaceFile(t, aliceWS, "team-x/project/plan.md", "# shared plan\n")
	// With files there, a narrower slice can be made.
	runCLI(t, aliceHome, t.TempDir(), "slice", "create", "team-x/project")
	members := runCLI(t, aliceHome, t.TempDir(), "account", "members", "team-x")
	if !strings.Contains(members, "alice-agent\towner") || !strings.Contains(members, "bob-agent\twriter") {
		t.Fatalf("members:\n%s", members)
	}
}
