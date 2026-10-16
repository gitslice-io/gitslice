package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

// TestOwnerReplacesALostAgentKey is the recovery path end to end: an agent
// loses its key, the human who claimed it makes a new one, the agent installs
// it, and the lost one is revoked.
func TestOwnerReplacesALostAgentKey(t *testing.T) {
	ctx := context.Background()
	const humanToken = "human-session-token"
	humanID := storage.ExternalSubjectID("owner")
	serverAddr, mem := startAgentSignupServerWithStores(t, true, map[string]string{humanToken: humanID})
	if _, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.SetVerifiedEmails(ctx, humanID, storage.ProviderService, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	run := func(r Runner, args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		r.Stdout, r.Stderr = &stdout, &stderr
		err := r.Run(ctx, args)
		if err != nil {
			return stdout.String() + stderr.String(), err
		}
		return stdout.String(), nil
	}

	// The agent registers and, later, loses the key it was given.
	agentHome := t.TempDir()
	agent := Runner{Home: agentHome}
	if out, err := run(agent, "auth", "register-agent", "--server", serverAddr, "--username", "release-bot", "--email", "owner@example.com", "--json"); err != nil {
		t.Fatalf("register-agent: %v\n%s", err, out)
	}
	var registered struct {
		SubjectID string `json:"subject_id"`
	}
	oldCfg, err := agent.readUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	registered.SubjectID = oldCfg.SubjectID

	human := Runner{Home: t.TempDir()}
	if err := human.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: humanToken, SubjectID: humanID}); err != nil {
		t.Fatal(err)
	}
	// Before claiming the agent, the human cannot make keys for it.
	if out, err := run(human, "auth", "keys", "create", "--agent", "release-bot"); err == nil || !strings.Contains(err.Error(), "no agent account you own") {
		t.Fatalf("create before the claim: err = %v\n%s", err, out)
	}
	if out, err := run(human, "claims", "accept", registered.SubjectID); err != nil {
		t.Fatalf("claims accept: %v\n%s", err, out)
	}

	// The human sees the one key, and makes a replacement.
	var listed struct {
		Account string         `json:"account"`
		Keys    []apiKeyOutput `json:"keys"`
	}
	out, err := run(human, "auth", "keys", "list", "--agent", "release-bot", "--json")
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || listed.Account != "release-bot" || len(listed.Keys) != 1 || !listed.Keys[0].Active {
		t.Fatalf("keys list = %q, %v", out, err)
	}
	lostKeyID := listed.Keys[0].ID
	secret, err := run(human, "auth", "keys", "create", "--agent", "release-bot", "--name", "after the loss")
	secret = strings.TrimSpace(secret)
	if err != nil || !strings.HasPrefix(secret, storage.APIKeyPrefix) || strings.ContainsAny(secret, " \n") {
		t.Fatalf("keys create should print the key alone on stdout: %q, %v", secret, err)
	}
	if text, err := run(human, "auth", "keys", "list", "--agent", "release-bot"); err != nil || strings.Contains(text, secret) || !strings.Contains(text, "after the loss") {
		t.Fatalf("a key listing must name keys and never show a secret:\n%s\n%v", text, err)
	}

	// The agent, on a machine with no credential, installs the new key.
	freshHome := t.TempDir()
	fresh := Runner{Home: freshHome}
	if out, err := run(Runner{Home: freshHome, Stdin: strings.NewReader("not-a-key\n")}, "auth", "use-key", "--server", serverAddr); err == nil || !strings.Contains(err.Error(), "that is not an API key") {
		t.Fatalf("a malformed key: err = %v\n%s", err, out)
	}
	if out, err := run(Runner{Home: freshHome, Stdin: strings.NewReader("")}, "auth", "use-key", "--server", serverAddr); err == nil || !strings.Contains(err.Error(), "no API key was given") {
		t.Fatalf("no key: err = %v\n%s", err, out)
	}
	if out, err := run(Runner{Home: freshHome, Stdin: strings.NewReader("gsk_madeup\n")}, "auth", "use-key", "--server", serverAddr); err == nil || !strings.Contains(err.Error(), "does not accept that key") {
		t.Fatalf("a key the server does not know: err = %v\n%s", err, out)
	}
	if _, err := fresh.readUserConfig(); err == nil {
		t.Fatal("a rejected key must not be saved")
	}
	if out, err := run(Runner{Home: freshHome, Stdin: strings.NewReader(secret + "\n")}, "auth", "use-key", "--server", serverAddr); err != nil || !strings.Contains(out, "signed in as "+registered.SubjectID) {
		t.Fatalf("use-key: %v\n%s", err, out)
	}
	if cfg, err := fresh.readUserConfig(); err != nil || cfg.Token != secret || cfg.SubjectID != registered.SubjectID {
		t.Fatalf("saved config = %+v, %v", cfg, err)
	}
	if out, err := run(fresh, "auth", "status", "--json"); err != nil || !strings.Contains(out, `"signed_in": true`) {
		t.Fatalf("the agent should be signed in with the new key: %v\n%s", err, out)
	}

	// The lost key is revoked, and stops working at once; the new one does not.
	if out, err := run(human, "auth", "keys", "revoke", lostKeyID, "--agent", "release-bot"); err != nil || !strings.Contains(out, "revoked API key "+lostKeyID) {
		t.Fatalf("revoke: %v\n%s", err, out)
	}
	if out, err := run(agent, "auth", "status", "--json"); err != nil || !strings.Contains(out, `"signed_in": false`) {
		t.Fatalf("the lost key still signs in: %v\n%s", err, out)
	}
	if out, err := run(fresh, "auth", "status", "--json"); err != nil || !strings.Contains(out, `"signed_in": true`) {
		t.Fatalf("the replacement key stopped working: %v\n%s", err, out)
	}

	// The agent can also make its own keys, with no --agent.
	if out, err := run(fresh, "auth", "keys", "create", "--name", "rotation"); err != nil || !strings.HasPrefix(strings.TrimSpace(out), storage.APIKeyPrefix) {
		t.Fatalf("an agent creating its own key: %v\n%s", err, out)
	}
	// A person who owns no agent named so is told what to do.
	if _, err := run(human, "auth", "keys", "create"); err == nil || !strings.Contains(err.Error(), "no agent account you own or are signed in as") {
		t.Fatalf("a human with no --agent: err = %v", err)
	}
}
