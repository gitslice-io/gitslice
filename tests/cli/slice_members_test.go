package cli_test

import (
	"strings"
	"testing"
)

// TestSliceShareBetweenAgents shares one slice of alice's with bob (writer)
// and carol (reader): bob checks it out and lands a change, carol reads but
// cannot land, neither reaches alice's other slices, and leaving ends it. A
// public slice checks out for anyone.
func TestSliceShareBetweenAgents(t *testing.T) {
	ts := startTestServer(t)
	homes, tokens := map[string]string{}, map[string]string{}
	for _, name := range []string{"alice-agent", "bob-agent", "carol-agent"} {
		token, _, subject := ts.provisionAccount(t, name, name)
		homes[name], tokens[name] = t.TempDir(), token
		writeCLIAuthConfig(t, homes[name], ts.addr, token, subject)
	}
	alice, bob, carol := homes["alice-agent"], homes["bob-agent"], homes["carol-agent"]

	aliceWS := t.TempDir()
	runCLI(t, alice, aliceWS, "workspace", "init", "alice-agent/home")
	writeWorkspaceFile(t, aliceWS, "alice-agent/project/plan.md", "# plan\n")
	writeWorkspaceFile(t, aliceWS, "alice-agent/private/notes.md", "secret\n")
	writeWorkspaceFile(t, aliceWS, "alice-agent/docs/readme.md", "# docs\n")
	runCLI(t, alice, aliceWS, "create", "--all", "--message", "start", "--submit")
	runCLI(t, alice, t.TempDir(), "slice", "create", "alice-agent/project")
	runCLI(t, alice, t.TempDir(), "slice", "create", "alice-agent/docs", "--visibility", "public")

	if _, err := runCLIResult(bob, t.TempDir(), "workspace", "init", "alice-agent/project"); err == nil {
		t.Fatal("bob checked out the slice before it was shared")
	}
	out := runCLI(t, alice, t.TempDir(), "slice", "share", "alice-agent/project", "bob-agent")
	if !strings.Contains(out, "shared alice-agent/project with bob-agent as writer") || !strings.Contains(out, "gs init alice-agent/project") {
		t.Fatalf("share:\n%s", out)
	}
	runCLI(t, alice, t.TempDir(), "slice", "share", "alice-agent/project", "carol-agent", "--role", "reader")
	members := runCLI(t, alice, t.TempDir(), "slice", "members", "alice-agent/project")
	if !strings.Contains(members, "bob-agent\twriter\t(person, added by alice-agent)") || !strings.Contains(members, "carol-agent\treader") {
		t.Fatalf("members:\n%s", members)
	}

	// bob finds it, checks it out, and lands a change.
	if list := runCLI(t, bob, t.TempDir(), "slice", "list"); !strings.Contains(list, "shared with you:\n  alice-agent:project (writer)") {
		t.Fatalf("bob's slice list:\n%s", list)
	}
	bobWS := t.TempDir()
	runCLI(t, bob, bobWS, "workspace", "init", "alice-agent/project")
	assertWorkspaceFile(t, bobWS, "alice-agent/project/plan.md", "# plan\n")
	writeWorkspaceFile(t, bobWS, "alice-agent/project/plan.md", "# plan\nstep 1\n")
	if out := runCLI(t, bob, bobWS, "create", "--all", "--message", "bob's step", "--submit"); !strings.Contains(out, "submitted ") {
		t.Fatalf("bob's change:\n%s", out)
	}
	// The slice's history is bob's to read (it is indexed shortly after a
	// submit, so the newest commit may not be listed yet).
	if out := runCLI(t, bob, bobWS, "log", "--limit", "5"); !strings.Contains(out, "start") {
		t.Fatalf("bob's log:\n%s", out)
	}
	// Git works the same: bob clones the shared slice, not alice's home.
	header := "http.extraHeader=Authorization: Bearer " + tokens["bob-agent"]
	clone := t.TempDir() + "/project"
	runGit(t, "", "-c", header, "clone", "http://"+ts.gitAddr+"/git/alice-agent/project.git", clone)
	assertWorkspaceFile(t, clone, "alice-agent/project/plan.md", "# plan\nstep 1\n")
	if _, _, err := runGitResult("", "-c", header, "clone", "http://"+ts.gitAddr+"/git/alice-agent/home.git", t.TempDir()+"/home"); err == nil {
		t.Fatal("bob cloned alice's home")
	}
	// Nothing else of alice's.
	if _, err := runCLIResult(bob, t.TempDir(), "workspace", "init", "alice-agent/home"); err == nil {
		t.Fatal("bob checked out alice's home")
	}

	// carol reads but cannot land.
	carolWS := t.TempDir()
	runCLI(t, carol, carolWS, "workspace", "init", "alice-agent/project")
	assertWorkspaceFile(t, carolWS, "alice-agent/project/plan.md", "# plan\nstep 1\n")
	writeWorkspaceFile(t, carolWS, "alice-agent/project/plan.md", "# plan\ncarol\n")
	if _, err := runCLIResult(carol, carolWS, "create", "--all", "--message", "carol's step"); err == nil {
		t.Fatal("a reader created a changeset")
	}
	// A public slice checks out for anyone, shared or not.
	docsWS := t.TempDir()
	runCLI(t, carol, docsWS, "workspace", "init", "alice-agent/docs")
	assertWorkspaceFile(t, docsWS, "alice-agent/docs/readme.md", "# docs\n")

	// Leaving and unsharing end it.
	if out := runCLI(t, bob, t.TempDir(), "slice", "leave", "alice-agent/project"); out != "left alice-agent/project\n" {
		t.Fatalf("leave: %q", out)
	}
	if _, err := runCLIResult(bob, t.TempDir(), "workspace", "init", "alice-agent/project"); err == nil {
		t.Fatal("bob checked out the slice after leaving")
	}
	runCLI(t, alice, t.TempDir(), "slice", "unshare", "alice-agent/project", "carol-agent")
	if out := runCLI(t, alice, t.TempDir(), "slice", "members", "alice-agent/project"); !strings.Contains(out, "is not shared with anyone outside account alice-agent") {
		t.Fatalf("members after:\n%s", out)
	}
}
