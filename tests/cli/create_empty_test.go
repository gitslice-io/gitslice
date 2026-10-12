package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCreateWithNothingChanged checks that gs create makes no changeset (and
// no dependency tree) when nothing changed, so a scheduled sync cannot land
// empty commits, and that --if-changed and --allow-empty choose otherwise.
func TestCreateWithNothingChanged(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	_, stderr := runCLIFails(t, home, workspace, "create", "--all", "--message", "daily sync")
	if !strings.Contains(stderr, "nothing changed since the base, so no changeset was created") || !strings.Contains(stderr, "--if-changed") {
		t.Fatalf("empty create: %s", stderr)
	}
	if _, stderr := runCLIFails(t, home, workspace, "deps"); !strings.Contains(stderr, "no active dependency tree") {
		t.Fatalf("an empty create left a dependency tree: %s", stderr)
	}
	if _, stderr := runCLIFails(t, home, workspace, "cs", "create", "--title", "daily sync"); !strings.Contains(stderr, "nothing changed") {
		t.Fatalf("empty cs create: %s", stderr)
	}

	// --if-changed: a quiet success, with or without --submit.
	if out := runCLI(t, home, workspace, "create", "--all", "--message", "daily sync", "--if-changed", "--submit"); out != "nothing changed; no changeset created\n" {
		t.Fatalf("--if-changed: %q", out)
	}
	var noop struct {
		Created bool   `json:"created"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, workspace, "create", "--all", "--message", "daily sync", "--if-changed", "--json")), &noop); err != nil || noop.Created || noop.Reason != "nothing_to_change" {
		t.Fatalf("--if-changed --json = %+v, %v", noop, err)
	}
	if _, stderr := runCLIFails(t, home, workspace, "create", "--all", "--message", "x", "--if-changed", "--allow-empty"); !strings.Contains(stderr, "not both") {
		t.Fatalf("both flags: %s", stderr)
	}

	// With a change, --if-changed creates and lands it as usual.
	writeWorkspaceFile(t, workspace, "daily.go", "package payment\nconst Daily = 1\n")
	if out := runCLI(t, home, workspace, "create", "--all", "--message", "daily sync", "--if-changed", "--submit"); !strings.HasPrefix(out, "created ") || !strings.Contains(out, "submitted ") {
		t.Fatalf("--if-changed with a change:\n%s", out)
	}

	// --allow-empty still creates an empty changeset on purpose.
	ws2 := t.TempDir()
	runCLI(t, home, ws2, "workspace", "init", "acme/payment")
	if out := runCLI(t, home, ws2, "create", "--all", "--message", "placeholder", "--allow-empty"); !strings.HasPrefix(out, "created ") {
		t.Fatalf("--allow-empty:\n%s", out)
	}
}
