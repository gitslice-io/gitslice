package cli_test

import (
	"encoding/json"
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
