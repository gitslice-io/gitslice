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

	if out := runCLI(t, home, payment, "trigger", "ls", "/acme/payment"); !strings.Contains(out, "no triggers apply at /acme/payment") {
		t.Fatalf("trigger ls with none = %q", out)
	}
	dir := filepath.Join(payment, "acme", "payment", ".gitslice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(`version: 1
triggers:
  hello:
    on: [manual, push]
    paths: ["**/*.go"]
    action:
      http:
        url: https://hooks.example.com/hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, payment, "create", "--all", "--message", "triggers")
	runCLI(t, home, payment, "submit")

	out := runCLI(t, home, payment, "trigger", "ls", "/acme/payment/api")
	if !strings.Contains(out, "payment/hello") || !strings.Contains(out, "on manual,push") || !strings.Contains(out, "http https://hooks.example.com/hello") {
		t.Fatalf("trigger ls = %q", out)
	}
	// From inside the workspace, the current folder is the default.
	if out := runCLI(t, home, filepath.Join(payment, "acme", "payment"), "trigger", "ls"); !strings.Contains(out, "payment/hello") {
		t.Fatalf("trigger ls in the workspace = %q", out)
	}
	out = runCLI(t, home, payment, "trigger", "run", "payment/hello", "--account", "acme", "--input", "reason=test")
	if !strings.Contains(out, "queued run trun_") {
		t.Fatalf("trigger run = %q", out)
	}
	runID := strings.Fields(out)[2]
	if out := runCLI(t, home, payment, "trigger", "runs", "--account", "acme"); !strings.Contains(out, runID) || !strings.Contains(out, "payment/hello") {
		t.Fatalf("trigger runs = %q", out)
	}
	if out := runCLI(t, home, payment, "trigger", "show", runID); !strings.Contains(out, "event: manual") {
		t.Fatalf("trigger show = %q", out)
	}
	_, stderr := runCLIFails(t, home, payment, "trigger", "run", "payment/missing", "--account", "acme")
	if !strings.Contains(stderr, "no trigger payment/missing") {
		t.Fatalf("trigger run of a missing trigger: %s", stderr)
	}
}
