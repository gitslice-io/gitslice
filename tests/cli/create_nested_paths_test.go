package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateLeavesOutACopiedWorkspace reproduces a workspace copied into
// itself: the copy has its own .gs, which gs create used to commit along with a
// second copy of the slice under a nested prefix.
func TestCreateLeavesOutACopiedWorkspace(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/payment/main.go", "package payment\n")
	runCLI(t, home, workspace, "create", "--all", "--message", "first", "--submit")

	writeWorkspaceFile(t, workspace, "acme/payment/copy/.gs/slice.json", "{}")
	writeWorkspaceFile(t, workspace, "acme/payment/copy/acme/payment/main.go", "package payment\n")
	writeWorkspaceFile(t, workspace, "acme/payment/deep/.gs/state.json", "{}")
	writeWorkspaceFile(t, workspace, "acme/payment/real.go", "package payment\nconst Real = 1\n")
	_, stderr := runCLIStreams(t, home, workspace, "create", "--all", "--message", "real change", "--submit")
	if !strings.Contains(stderr, "skipping acme/payment/copy: it is another gs workspace") {
		t.Fatalf("no warning about the copied workspace: %q", stderr)
	}

	verify := t.TempDir()
	runCLI(t, home, verify, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verify, "acme/payment/real.go", "package payment\nconst Real = 1\n")
	for _, rel := range []string{"acme/payment/copy", "acme/payment/deep/.gs"} {
		if _, err := os.Stat(filepath.Join(verify, filepath.FromSlash(rel))); err == nil {
			t.Fatalf("%s was committed", rel)
		}
	}
}

// TestCreateRefusesTheSlicePathRepeated: files that sit under the slice's own
// path a second time (a slice copied into itself) split the slice, so with no
// one to ask gs create stops and --yes goes ahead.
func TestCreateRefusesTheSlicePathRepeated(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/payment/main.go", "package payment\n")
	runCLI(t, home, workspace, "create", "--all", "--message", "first", "--submit")

	depsBefore := runCLI(t, home, workspace, "deps")
	writeWorkspaceFile(t, workspace, "acme/payment/acme/payment/main.go", "package payment\n")
	_, stderr := runCLIFails(t, home, workspace, "create", "--all", "--message", "nested copy")
	for _, want := range []string{"repeats the slice path", "under the slice's own path a second time", "--yes"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr is missing %q:\n%s", want, stderr)
		}
	}
	if depsAfter := runCLI(t, home, workspace, "deps"); depsAfter != depsBefore {
		t.Fatalf("the refused create changed the dependency tree:\nbefore:\n%s\nafter:\n%s", depsBefore, depsAfter)
	}
	if out := runCLI(t, home, workspace, "create", "--all", "--message", "nested copy", "--yes"); !strings.HasPrefix(out, "created ") {
		t.Fatalf("--yes did not create:\n%s", out)
	}
}
