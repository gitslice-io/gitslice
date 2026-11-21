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

func TestReusableTemplatesAndPipelinesCLI(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	ws := t.TempDir()
	loginTestCLI(t, ts, home, ws)
	runCLI(t, home, ws, "workspace", "init", "acme/payment")

	dir := filepath.Join(ws, "acme", "payment", ".gitslice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "automations.yaml"), []byte(`version: 1
automations:
  notify-template:
    description: Reusable webhook notifier
    on: [call]
    inputs:
      channel: { type: string, required: true }
      env: { type: choice, options: [staging, prod], default: staging }
    action:
      http:
        url: https://hooks.example.com/notify/${{ inputs.channel }}?env=${{ inputs.env }}

  notify-ops:
    on: [manual]
    action:
      uses: /acme/payment/notify-template
      with:
        channel: ops

  release-pipe:
    on: [manual]
    action:
      steps:
        - id: first
          uses: /acme/payment/notify-template
          with:
            channel: build
        - id: second
          if: always
          http:
            url: https://hooks.example.com/done
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, ws, "create", "--all", "--message", "reusable and pipeline")
	runCLI(t, home, ws, "submit")

	// gs auto ls shows uses and steps summaries.
	lsOut := runCLI(t, home, ws, "auto", "ls", "/acme/payment")
	if !strings.Contains(lsOut, "uses /acme/payment/notify-template") {
		t.Fatalf("auto ls missing uses summary:\n%s", lsOut)
	}
	if !strings.Contains(lsOut, "steps 2 steps") {
		t.Fatalf("auto ls missing steps summary:\n%s", lsOut)
	}

	// gs auto inspect resolves the reusable on:[call] template and displays its inputs.
	inspectOut := runCLI(t, home, ws, "auto", "inspect", "/acme/payment/notify-template")
	for _, want := range []string{
		"template: payment/notify-template",
		"description: Reusable webhook notifier",
		"channel (string, required)",
		"env (choice, optional, default=staging, options=staging|prod)",
	} {
		if !strings.Contains(inspectOut, want) {
			t.Fatalf("auto inspect missing %q:\n%s", want, inspectOut)
		}
	}

	// gs auto run on uses caller accepts inherited unbound input (env=prod).
	runOut := runCLI(t, home, ws, "auto", "run", "notify-ops", "-i", "env=prod")
	if !strings.Contains(runOut, "queued run trun_") {
		t.Fatalf("auto run notify-ops = %q", runOut)
	}
	usesRunID := strings.Fields(runOut)[2]
	showUses := runCLI(t, home, ws, "auto", "show", usesRunID)
	if !strings.Contains(showUses, "template: /acme/payment/notify-template") {
		t.Fatalf("auto show uses run missing template provenance:\n%s", showUses)
	}

	// gs auto run on pipeline starts parent run; gs auto show lists step(s).
	pipeOut := runCLI(t, home, ws, "auto", "run", "release-pipe")
	if !strings.Contains(pipeOut, "queued run trun_") {
		t.Fatalf("auto run release-pipe = %q", pipeOut)
	}
	pipeRunID := strings.Fields(pipeOut)[2]
	showPipe := runCLI(t, home, ws, "auto", "show", pipeRunID)
	if !strings.Contains(showPipe, "automation: payment/release-pipe (steps)") {
		t.Fatalf("auto show pipeline run = %q", showPipe)
	}
}
