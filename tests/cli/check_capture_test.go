package cli_test

import (
	"strings"
	"testing"
)

func TestChangesetCaptureBundlesChecksForSubmitGate(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	runCLI(t, home, workspace, "slice", "update", "acme/payment", "--required-check", "acme/payment/check")

	writeWorkspaceFile(t, workspace, ".gitslice/checks.yaml", strings.TrimSpace(`
version: 1
checks:
  check:
    run: "true"
`)+"\n")
	writeWorkspaceFile(t, workspace, "capture_check_pass.go", "package payment\nconst CaptureCheckPass = true\n")
	capture := runCLI(t, home, workspace, "cs", "capture", "--title", "capture check pass")
	if !strings.Contains(capture, "check acme/payment/check: passed") {
		t.Fatalf("capture output missing passing check summary:\n%s", capture)
	}
	runCLI(t, home, workspace, "cs", "submit")

	writeWorkspaceFile(t, workspace, ".gitslice/checks.yaml", strings.TrimSpace(`
version: 1
checks:
  check:
    run: "false"
`)+"\n")
	writeWorkspaceFile(t, workspace, "capture_check_fail.go", "package payment\nconst CaptureCheckFail = true\n")
	capture = runCLI(t, home, workspace, "cs", "capture", "--title", "capture check fail")
	if !strings.Contains(capture, "check acme/payment/check: failed") {
		t.Fatalf("capture output missing failing check summary:\n%s", capture)
	}
	_, stderr := runCLIFails(t, home, workspace, "cs", "submit")
	if !strings.Contains(stderr, `required check "acme/payment/check" is failing`) {
		t.Fatalf("submit stderr missing failing required check:\n%s", stderr)
	}
}
