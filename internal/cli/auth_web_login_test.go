package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestAuthWebLoginMintsScopedRevocableLink(t *testing.T) {
	ctx := context.Background()
	serverAddr, _ := startAgentSignupServerWithStores(t, true, nil)

	run := func(r Runner, args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		r.Stdout, r.Stderr = &stdout, &stderr
		err := r.Run(ctx, args)
		return stdout.String(), stderr.String(), err
	}

	agent := Runner{Home: t.TempDir()}
	if out, errOut, err := run(agent, "auth", "register-agent", "--server", serverAddr, "--username", "web-bot", "--email", "owner@example.com", "--json"); err != nil {
		t.Fatalf("register-agent: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}

	// Default text mode: URL alone on stdout, metadata and revoke command on stderr.
	stdout, stderr, err := run(agent, "auth", "web-login", "--web-url", "https://gitslice.io", "--expires", "2h", "--role", "reader", "--path", "/accounts/web-bot")
	if err != nil {
		t.Fatalf("web-login text: %v\nstderr=%s", err, stderr)
	}
	rawURL := strings.TrimSpace(stdout)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse login URL %q: %v", rawURL, err)
	}
	if parsed.Scheme != "https" || parsed.Host != "gitslice.io" || parsed.Path != "/agent-login" {
		t.Fatalf("unexpected login URL %q", rawURL)
	}
	if parsed.Query().Get("returnTo") != "/accounts/web-bot" {
		t.Fatalf("returnTo = %q, want /accounts/web-bot", parsed.Query().Get("returnTo"))
	}
	fragParams, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("parse fragment %q: %v", parsed.Fragment, err)
	}
	secret := fragParams.Get("key")
	if !strings.HasPrefix(secret, storage.APIKeyPrefix) {
		t.Fatalf("fragment key = %q, want %s prefix", secret, storage.APIKeyPrefix)
	}
	if !strings.Contains(stderr, "gs auth keys revoke ") {
		t.Fatalf("stderr missing revoke hint: %q", stderr)
	}

	// JSON output carries structured fields and creates a web-purpose child key.
	opened := ""
	origOpen := openWebLoginBrowser
	openWebLoginBrowser = func(u string) error {
		opened = u
		return nil
	}
	t.Cleanup(func() { openWebLoginBrowser = origOpen })

	jsonOut, errOut, err := run(agent, "auth", "web-login", "--web-url", "https://gitslice.io", "--open", "--json")
	if err != nil {
		t.Fatalf("web-login json: %v\nstderr=%s", err, errOut)
	}
	var got authWebLoginOutput
	if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
		t.Fatalf("unmarshal json %q: %v", jsonOut, err)
	}
	if got.Account != "web-bot" || got.Key.Purpose != webLoginKeyPurpose || got.ExpiresAt == "" {
		t.Fatalf("unexpected web-login output: %+v", got)
	}
	if opened != got.URL {
		t.Fatalf("--open received %q, want %q", opened, got.URL)
	}

	// The minted key works as a valid API key and can revoke itself on logout.
	webSessionRunner := Runner{Home: t.TempDir()}
	if err := webSessionRunner.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      secret,
		SubjectID:  got.AgentSubjectID,
	}); err != nil {
		t.Fatal(err)
	}
	statusOut, _, err := run(webSessionRunner, "auth", "status", "--json")
	if err != nil || !strings.Contains(statusOut, `"signed_in": true`) {
		t.Fatalf("web key auth status failed: %v\n%s", err, statusOut)
	}

	// Rejects unsafe --path values (preventing open redirects).
	if _, _, err := run(agent, "auth", "web-login", "--path", "https://evil.example"); err == nil {
		t.Fatal("expected error for external --path")
	}
	if _, _, err := run(agent, "auth", "web-login", "--path", "//evil.example"); err == nil {
		t.Fatal("expected error for scheme-relative --path")
	}
}
