package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeLocalAutomation(t *testing.T, script string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "acme", "ops", ".gitslice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "automations.yaml"), []byte(`version: 1
automations:
  hook:
    on: [manual]
    secrets: [TOKEN]
    action:
      worker: hook.js
  other:
    on: [manual]
    action:
      http:
        url: https://example.com/hook
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hook.js"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFindLocalAutomation(t *testing.T) {
	root := writeLocalAutomation(t, "export default {};\n")
	folder, gotRoot, name, auto, err := findLocalAutomation(filepath.Join(root, "acme", "ops", ".gitslice"), "acme", "ops/hook")
	if err != nil {
		t.Fatal(err)
	}
	if folder != filepath.Join(root, "acme", "ops") || gotRoot != root || name != "hook" || auto.Action.Worker != "hook.js" {
		t.Fatalf("found folder=%s root=%s name=%s worker=%s", folder, gotRoot, name, auto.Action.Worker)
	}
	if _, _, _, _, err := findLocalAutomation(filepath.Join(root, "acme", "ops"), "acme", "web/hook"); err == nil {
		t.Fatal("an automation in another folder must not be found")
	}
	if _, _, _, _, err := findLocalAutomation(filepath.Join(root, "acme", "ops"), "other", "ops/hook"); err == nil {
		t.Fatal("an automation of another account must not be found")
	}
}

func TestRunLocalAutomation(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	root := writeLocalAutomation(t, `export default {
  async onEvent(event, gs) {
    console.log("token " + gs.secret("TOKEN") + " input " + event.inputs?.version);
    gs.waitForCallback();
  },
  async onCallback(req, gs) {
    const body = await req.json();
    if (body.ok) gs.finish(); else gs.fail("not ok");
  },
};
`)
	cb := func(body string) string {
		p := filepath.Join(t.TempDir(), "cb.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var out, errOut bytes.Buffer
	r := Runner{Dir: filepath.Join(root, "acme", "ops"), Stdout: &out, Stderr: &errOut}
	err := r.runLocalAutomation(context.Background(), commandOptions{}, "acme", "ops/hook",
		[]string{"version=1.2"}, []string{"TOKEN=t-1"}, []string{cb(`{"ok":true}`)})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, errOut.String())
	}
	// The script sees the placeholder, never the value.
	if !strings.Contains(out.String(), "token gs-local-secret:TOKEN input 1.2") || strings.Contains(out.String(), "t-1") {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "run succeeded") {
		t.Fatalf("stderr = %q", errOut.String())
	}

	errOut.Reset()
	err = r.runLocalAutomation(context.Background(), commandOptions{}, "acme", "ops/hook", nil, nil, []string{cb(`{"ok":false}`)})
	if err == nil || !strings.Contains(errOut.String(), "run failed: not ok") {
		t.Fatalf("failed run: err=%v stderr=%q", err, errOut.String())
	}

	if err := r.runLocalAutomation(context.Background(), commandOptions{}, "acme", "ops/other", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "worker") {
		t.Fatalf("http automation: err = %v, want a not-a-worker error", err)
	}
	if err := r.runLocalAutomation(context.Background(), commandOptions{}, "acme", "ops/hook", nil, []string{"NOPE=1"}, nil); err == nil {
		t.Fatal("a secret the automation does not list must be refused")
	}
}
