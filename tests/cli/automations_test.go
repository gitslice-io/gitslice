package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTriggerCommands lists triggers and runs through gs trigger
// (design/34_triggers.md).
func TestTriggerCommands(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")

	if out := runCLI(t, home, payment, "auto", "ls", "/acme/payment"); !strings.Contains(out, "no automations apply at /acme/payment") {
		t.Fatalf("auto ls with none = %q", out)
	}
	dir := filepath.Join(payment, "acme", "payment", ".gitslice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "automations.yaml"), []byte(`version: 1
automations:
  hello:
    on: [manual, push]
    paths: ["**/*.go"]
    action:
      http:
        url: https://hooks.example.com/hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, payment, "create", "--all", "--message", "automations")
	runCLI(t, home, payment, "submit")

	out := runCLI(t, home, payment, "auto", "ls", "/acme/payment/api")
	if !strings.Contains(out, "payment/hello") || !strings.Contains(out, "on manual,push") || !strings.Contains(out, "http https://hooks.example.com/hello") {
		t.Fatalf("auto ls = %q", out)
	}
	// From inside the workspace (both root and folder), the workspace folder is the default.
	if out := runCLI(t, home, payment, "automation", "ls"); !strings.Contains(out, "payment/hello") {
		t.Fatalf("automation ls at workspace root = %q", out)
	}
	if out := runCLI(t, home, filepath.Join(payment, "acme", "payment"), "automation", "ls"); !strings.Contains(out, "payment/hello") {
		t.Fatalf("automation ls in the workspace = %q", out)
	}
	// Inside the workspace, gs auto run resolves the workspace's account and unqualified automation name.
	out = runCLI(t, home, payment, "auto", "run", "hello", "--input", "reason=test")
	if !strings.Contains(out, "queued run trun_") {
		t.Fatalf("auto run unqualified = %q", out)
	}
	runID := strings.Fields(out)[2]
	if out := runCLI(t, home, payment, "auto", "runs"); !strings.Contains(out, runID) || !strings.Contains(out, "payment/hello") {
		t.Fatalf("auto runs without --account = %q", out)
	}
	if out := runCLI(t, home, payment, "auto", "runs", "--automation", "hello"); !strings.Contains(out, runID) || !strings.Contains(out, "payment/hello") {
		t.Fatalf("auto runs --automation unqualified = %q", out)
	}
	if out := runCLI(t, home, payment, "auto", "show", runID); !strings.Contains(out, "event: manual") {
		t.Fatalf("auto show = %q", out)
	}
	_, stderr := runCLIFails(t, home, payment, "auto", "run", "payment/missing", "--account", "acme")
	if !strings.Contains(stderr, "no automation payment/missing") {
		t.Fatalf("auto run of a missing automation: %s", stderr)
	}
}
