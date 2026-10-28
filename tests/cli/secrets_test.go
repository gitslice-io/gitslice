package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretsScopedToSlices manages an account secret with the CLI and checks
// how the per-slice commands see it.
func TestSecretsScopedToSlices(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")

	out, _ := runCLIStreamsWithInput(t, home, payment, "s3cret\n", "secret", "create", "DEPLOY_TOKEN", "--account", "acme", "--slice", "payment", "--json")
	var created struct {
		ID        string   `json:"id"`
		Account   string   `json:"account"`
		Name      string   `json:"name"`
		AllSlices bool     `json:"all_slices"`
		Slices    []string `json:"slices"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("secret create --json: %v\n%s", err, out)
	}
	if created.ID == "" || created.Account != "acme" || created.Name != "DEPLOY_TOKEN" || created.AllSlices || strings.Join(created.Slices, ",") != "payment" {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(out, "s3cret") {
		t.Fatal("secret create printed the value")
	}

	out = runCLI(t, home, payment, "secret", "update", created.ID, "--add-slice", "acme/home")
	if !strings.Contains(out, "home,payment") {
		t.Fatalf("secret update --add-slice = %q", out)
	}
	out = runCLI(t, home, payment, "secret", "ls", "--account", "acme")
	if !strings.Contains(out, created.ID) || !strings.Contains(out, "DEPLOY_TOKEN") || !strings.Contains(out, "home,payment") {
		t.Fatalf("secret ls = %q", out)
	}

	// The slice commands see it, and do not change a shared value.
	if out := runCLI(t, home, payment, "slice", "secret", "ls", "acme/payment"); strings.TrimSpace(out) != "DEPLOY_TOKEN" {
		t.Fatalf("slice secret ls = %q", out)
	}
	_, stderr := runCLIFails(t, home, payment, "slice", "secret", "set", "acme/payment", "DEPLOY_TOKEN", "other")
	if !strings.Contains(stderr, "shared with 1 other slice") {
		t.Fatalf("slice secret set of a shared secret: %s", stderr)
	}
	runCLI(t, home, payment, "slice", "secret", "rm", "acme/payment", "DEPLOY_TOKEN")
	out = runCLI(t, home, payment, "secret", "ls", "--account", "acme", "--json")
	var listed struct {
		Secrets []struct {
			ID     string   `json:"id"`
			Slices []string `json:"slices"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("secret ls --json: %v\n%s", err, out)
	}
	if len(listed.Secrets) != 1 || strings.Join(listed.Secrets[0].Slices, ",") != "home" {
		t.Fatalf("after slice secret rm: %+v", listed)
	}

	// A value piped to gs slice secret set makes a secret of that slice only.
	runCLIStreamsWithInput(t, home, payment, "mine\n", "slice", "secret", "set", "acme/payment", "OWN_TOKEN")
	_, stderr = runCLIFails(t, home, payment, "secret", "create", "X", "--account", "acme")
	if !strings.Contains(stderr, "--all-slices") {
		t.Fatalf("secret create without a scope: %s", stderr)
	}
	runCLI(t, home, payment, "secret", "rm", created.ID)
	if out := runCLI(t, home, payment, "slice", "secret", "ls", "acme/payment"); strings.TrimSpace(out) != "OWN_TOKEN" {
		t.Fatalf("slice secret ls at the end = %q", out)
	}
}

// TestSecretEnvFillsATemplate fills an env template from the slice's secrets
// into a file only its owner can read.
func TestSecretEnvFillsATemplate(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")
	runCLIStreamsWithInput(t, home, payment, "it's s3cret\n", "secret", "create", "DEPLOY_TOKEN", "--account", "acme", "--slice", "payment")

	dir := t.TempDir()
	template := filepath.Join(dir, "env.prod.template")
	if err := os.WriteFile(template, []byte("API=https://api.example.com\nDEPLOY_TOKEN=gitslice-secret:DEPLOY_TOKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, ".env.prod")
	runCLI(t, home, payment, "secret", "env", template, "-o", out)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "API=https://api.example.com\nDEPLOY_TOKEN='it'\\''s s3cret'\n"; string(got) != want {
		t.Fatalf("filled env = %q, want %q", got, want)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}

	_, stderr := runCLIFails(t, home, payment, "secret", "env", template, "-o", out)
	if !strings.Contains(stderr, "--force") {
		t.Fatalf("second run without --force: %s", stderr)
	}
	runCLI(t, home, payment, "secret", "env", template, "-o", out, "--force")

	if err := os.WriteFile(template, []byte("X=gitslice-secret:NOT_THERE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr = runCLIFails(t, home, payment, "secret", "env", template, "-o", filepath.Join(dir, "other"))
	if !strings.Contains(stderr, "NOT_THERE") {
		t.Fatalf("missing secret: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); !os.IsNotExist(err) {
		t.Fatal("a template with a missing secret wrote a file")
	}
}
