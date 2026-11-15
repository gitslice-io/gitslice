package cli_test

import (
	"regexp"
	"strings"
	"testing"
)

// TestShowChangesetAndModifySubmit covers fb_d155: gs show takes a changeset
// as well as a commit, -m is --message, and gs modify --submit lands the new
// patchset.
func TestShowChangesetAndModifySubmit(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspace, "ergonomics.go", "package payment\nconst Ergonomics = 1\n")
	out := runCLI(t, home, workspace, "create", "--all", "-m", "ergonomics first")
	handle := regexp.MustCompile(`created ([0-9a-f]{10}) patchset 1`).FindStringSubmatch(out)
	if handle == nil {
		t.Fatalf("create output has no changeset handle:\n%s", out)
	}
	show := runCLI(t, home, workspace, "show", handle[1])
	if !strings.Contains(show, "title: ergonomics first") || !strings.Contains(show, "status: draft") {
		t.Fatalf("gs show <changeset> =\n%s", show)
	}

	writeWorkspaceFile(t, workspace, "ergonomics.go", "package payment\nconst Ergonomics = 2\n")
	out, stderr := runCLIStreams(t, home, workspace, "modify", "--all", "-m", "ergonomics second", "--submit")
	if !strings.Contains(out+stderr, "submitted "+handle[1]) {
		t.Fatalf("gs modify --submit did not submit:\nstdout:\n%s\nstderr:\n%s", out, stderr)
	}
	if show := runCLI(t, home, workspace, "show", handle[1]); !strings.Contains(show, "title: ergonomics second") || !strings.Contains(show, "status: submitted") {
		t.Fatalf("after modify --submit, gs show =\n%s", show)
	}
	if help := runCLI(t, home, workspace, "--help"); !regexp.MustCompile(`(?m)^  cs\s`).MatchString(help) {
		t.Fatalf("gs --help does not list cs:\n%s", help)
	}
}
