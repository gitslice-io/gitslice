package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/agentd"
)

func TestSanitizedAgentRuntimeEnvScrubsSecrets(t *testing.T) {
	t.Setenv("GS_AGENT_ALLOW_GS_TOKEN", "")
	in := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/tmp/home",
		"OPENAI_API_KEY=sk-user-key",
		"GS_TOKEN=gs_tok_secret",
		"GITSLICE_RUNNER_TOKEN=runner_secret",
		"GITSLICE_SECRETS_KEY=master_key",
		"CLERK_SECRET_KEY=sk_live_clerk",
		"GS_SERVICE_PRIVATE_KEY=pem",
	}
	got := agentd.SanitizedRuntimeEnv(in)
	joined := "\n" + strings.Join(got, "\n") + "\n"
	for _, forbidden := range []string{
		"GS_TOKEN=",
		"GITSLICE_RUNNER_TOKEN=",
		"GITSLICE_SECRETS_KEY=",
		"CLERK_SECRET_KEY=",
		"GS_SERVICE_PRIVATE_KEY=",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("sanitized env leaked %s: %v", forbidden, got)
		}
	}
	for _, allowed := range []string{"PATH=", "HOME=", "OPENAI_API_KEY="} {
		if !strings.Contains(joined, allowed) {
			t.Fatalf("sanitized env dropped %s: %v", allowed, got)
		}
	}
}

func TestRequireContainerChecksDisablesHostExecAndHostWorkspaceChecks(t *testing.T) {
	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "")
	if requireContainerChecks() {
		t.Fatal("expected requireContainerChecks() == false by default")
	}
	msgDefault := agentd.RegisterDaemonMessage("d1", "ci", "dev", "inst1", 2, !requireContainerChecks())
	if !msgDefault.GetRegister().GetAllowHostExec() {
		t.Fatal("expected AllowHostExec == true when GITSLICE_CHECKS_REQUIRE_CONTAINER is unset")
	}

	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "1")
	if !requireContainerChecks() {
		t.Fatal("expected requireContainerChecks() == true when GITSLICE_CHECKS_REQUIRE_CONTAINER=1")
	}
	msgContainerOnly := agentd.RegisterDaemonMessage("d1", "ci", "dev", "inst1", 2, !requireContainerChecks())
	if msgContainerOnly.GetRegister().GetAllowHostExec() {
		t.Fatal("expected AllowHostExec == false when GITSLICE_CHECKS_REQUIRE_CONTAINER=1")
	}

	workspace := t.TempDir()
	marker := filepath.Join(t.TempDir(), "host-ran")
	writeCIWorkspaceForTest(t, workspace, "touch "+marker)

	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"ci"}); err == nil {
		t.Fatal("expected gs ci host check to fail when GITSLICE_CHECKS_REQUIRE_CONTAINER=1")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("host shell command executed despite GITSLICE_CHECKS_REQUIRE_CONTAINER=1")
	}
}

func TestWorkspaceDiskTreeReaderRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	secretPath := filepath.Join(parent, "secret-checks.yaml")
	if err := os.WriteFile(secretPath, []byte("version: 1\nchecks:\n  pwn:\n    run: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workspace := filepath.Join(parent, "ws")
	gitsliceDir := filepath.Join(workspace, "acme", "payment", ".gitslice")
	if err := os.MkdirAll(gitsliceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(gitsliceDir, "checks.yaml")); err != nil {
		t.Fatal(err)
	}

	reader := workspaceDiskTreeReader{
		root:          workspace,
		includedPaths: []string{"/acme/payment"},
	}
	if _, err := reader.ReadFile(context.Background(), "", "/acme/payment/.gitslice/checks.yaml"); err == nil {
		t.Fatal("workspaceDiskTreeReader.ReadFile followed symlink outside workspace root")
	}
}
