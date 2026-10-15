package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerShellInspectsServerFilesWithoutLocalFile(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "server_only.go", "package payment\nconst ServerOnly = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell seed")
	runCLI(t, home, workspace, "cs", "submit")
	if err := os.Remove(filepath.Join(workspace, "server_only.go")); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := runCLIStreamsWithInput(t, home, workspace, strings.Join([]string{
		"pwd",
		"ls",
		"cat server_only.go",
		"stat server_only.go",
		"quit",
	}, "\n")+"\n", "shell")
	if stderr != "" {
		t.Fatalf("expected empty shell stderr, got:\n%s", stderr)
	}
	for _, want := range []string{
		"server shell: acme:payment @",
		"gs acme:payment:/> /",
		"server_only.go",
		"package payment\nconst ServerOnly = true\n",
		"kind: file",
		"shell_path: /server_only.go",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("shell output missing %q:\n%s", want, stdout)
		}
	}
}

func TestServerShellNavigationAndSliceBoundary(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "nested/a.go", "package nested\nconst A = true\n")
	writeWorkspaceFile(t, workspace, "nested/deep/b.go", "package deep\nconst B = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell nested")
	runCLI(t, home, workspace, "cs", "submit")

	stdout, stderr := runCLIStreamsWithInput(t, home, workspace, strings.Join([]string{
		"ls /",
		"cd nested",
		"pwd",
		"ls",
		"cat a.go",
		"cat /nested/deep/b.go",
		"stat /acme/payment/nested/deep/b.go",
		"cat /acme/backend/secret.go",
		"quit",
	}, "\n")+"\n", "shell")
	for _, want := range []string{
		"nested/",
		"gs acme:payment:/nested> /nested",
		"a.go",
		"deep/",
		"package nested\nconst A = true\n",
		"package deep\nconst B = true\n",
		"shell_path: /nested/deep/b.go",
		"kind: file",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("shell output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "outside the workspace slice") {
		t.Fatalf("expected outside-slice error in stderr, got:\n%s", stderr)
	}
}

func TestServerShellCommitPinning(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "versioned.go", "package payment\nconst Version = 1\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell v1")
	firstSubmit := runCLI(t, home, workspace, "cs", "submit", "--json")
	firstCommitID := submittedRefCommitID(t, firstSubmit)

	writeWorkspaceFile(t, workspace, "versioned.go", "package payment\nconst Version = 2\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell v2")
	runCLI(t, home, workspace, "cs", "submit")

	pinned, pinnedStderr := runCLIStreamsWithInput(t, home, workspace, "cat versioned.go\nquit\n", "shell", "--commit", firstCommitID)
	if pinnedStderr != "" {
		t.Fatalf("expected empty pinned shell stderr, got:\n%s", pinnedStderr)
	}
	if !strings.Contains(pinned, "const Version = 1") || strings.Contains(pinned, "const Version = 2") {
		t.Fatalf("expected pinned shell to show version 1 only, got:\n%s", pinned)
	}
	current, currentStderr := runCLIStreamsWithInput(t, home, workspace, "cat versioned.go\nquit\n", "shell")
	if currentStderr != "" {
		t.Fatalf("expected empty current shell stderr, got:\n%s", currentStderr)
	}
	if !strings.Contains(current, "const Version = 2") {
		t.Fatalf("expected current shell to show version 2, got:\n%s", current)
	}
}

func TestServerShellRunsOutsideWorkspace(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	outsideWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "global_shell.go", "package payment\nconst GlobalShell = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "global shell seed")
	runCLI(t, home, workspace, "cs", "submit")

	// Outside a workspace, the shell attaches to the signed-in user's personal
	// home slice (the test identity's personal account is acme), scoped at the
	// global root with the account projected in.
	stdout, stderr := runCLIStreamsWithInput(t, home, outsideWorkspace, strings.Join([]string{
		"pwd",
		"ls /",
		"ls /acme",
		"cd acme/payment",
		"pwd",
		"cat global_shell.go",
		"stat /acme/payment/global_shell.go",
		"quit",
	}, "\n")+"\n", "shell")
	if stderr != "" {
		t.Fatalf("expected empty shell stderr, got:\n%s", stderr)
	}
	for _, want := range []string{
		"server shell: acme:home @",
		"gs /> /",
		"acme/",
		"payment/",
		"gs /acme/payment> /acme/payment",
		"package payment\nconst GlobalShell = true\n",
		"shell_path: /acme/payment/global_shell.go",
		"kind: file",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("global shell output missing %q:\n%s", want, stdout)
		}
	}
}

func TestServerShellAttachesExplicitSlice(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	outsideWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "explicit.go", "package payment\nconst ExplicitShell = true\n")
	writeWorkspaceFile(t, workspace, "custom/nested.go", "package custom\nconst Nested = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "explicit shell seed")
	runCLI(t, home, workspace, "cs", "submit")
	runCLI(t, home, outsideWorkspace, "slice", "create", "acme/new-slice", "--include", "/acme/payment/custom")

	stdout, stderr := runCLIStreamsWithInput(t, home, outsideWorkspace, strings.Join([]string{
		"pwd",
		"ls",
		"cd acme",
		"pwd",
		"ls",
		"cd payment",
		"ls",
		"cd custom",
		"pwd",
		"cat nested.go",
		"cat ../explicit.go",
		"mkdir explicit-dir",
		"write explicit-dir/from_shell.txt hello explicit slice",
		"cat explicit-dir/from_shell.txt",
		"cat /acme/backend/secret.go",
		"quit",
	}, "\n")+"\n", "shell", "--slice", "acme/new-slice")
	for _, want := range []string{
		"server shell: acme:new-slice @",
		"gs acme:new-slice:/> /",
		"gs acme:new-slice:/acme> /acme",
		"gs acme:new-slice:/acme/payment/custom> /acme/payment/custom",
		"acme/",
		"payment/",
		"custom/",
		"package custom\nconst Nested = true\n",
		"ok created through changeset",
		"ok wrote through changeset",
		"hello explicit slice",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("explicit slice shell output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "outside the attached slice") {
		t.Fatalf("expected explicit slice boundary error in stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "/acme/payment/explicit.go") {
		t.Fatalf("expected custom projection to reject sibling file, got:\n%s", stderr)
	}
}
