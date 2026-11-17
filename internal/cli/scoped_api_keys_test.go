package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestCLIScopedAndChildAPIKeys(t *testing.T) {
	ctx := context.Background()
	const humanToken = "human-cli-session"
	humanID := storage.ExternalSubjectID("alice")
	serverAddr, mem := startAgentSignupServerWithStores(t, true, map[string]string{humanToken: humanID})
	if _, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "alice", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, humanID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.Create(ctx, humanID, &corev1.SliceRef{Account: "alice", Slice: "project"}, []string{"/alice/project"}, "private", 0, nil); err != nil {
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

	human := Runner{Home: t.TempDir()}
	if err := human.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: humanToken, SubjectID: humanID}); err != nil {
		t.Fatal(err)
	}

	// Bad flags (including overflowing day durations) are rejected locally before network round-trip.
	for _, badArgs := range [][]string{
		{"auth", "keys", "create", "--expires", "0s"},
		{"auth", "keys", "create", "--expires", "-7d"},
		{"auth", "keys", "create", "--expires", "106752d"},
		{"auth", "keys", "create", "--expires", "999999999d"},
		{"auth", "keys", "create", "--expires", "notaduration"},
		{"auth", "keys", "create", "--role", "admin"},
	} {
		if _, err := run(human, badArgs...); err == nil {
			t.Fatalf("expected error for %v", badArgs)
		}
	}

	// Create a scoped, role-capped, expiring personal key.
	out, err := run(human, "auth", "keys", "create",
		"--name", "deploy-bot-key",
		"--slice", "alice/project",
		"--role", "contributor",
		"--expires", "7d",
		"--purpose", "ci checks",
		"--json",
	)
	if err != nil {
		t.Fatalf("keys create: %v\n%s", err, out)
	}
	var createdResp struct {
		Account string       `json:"account"`
		Key     apiKeyOutput `json:"key"`
		Secret  string       `json:"secret"`
	}
	if err := json.Unmarshal([]byte(out), &createdResp); err != nil {
		t.Fatalf("unmarshal keys create json: %v\n%s", err, out)
	}
	if createdResp.Account != "alice" ||
		createdResp.Key.MaxRole != "contributor" ||
		createdResp.Key.Purpose != "ci checks" ||
		createdResp.Key.ExpiresAt == "" ||
		len(createdResp.Key.Slices) != 1 || createdResp.Key.Slices[0] != "alice/project" ||
		!strings.HasPrefix(createdResp.Secret, storage.APIKeyPrefix) {
		t.Fatalf("unexpected create json = %+v", createdResp)
	}

	// Install the scoped key via use-key, then list & mint a child key.
	scopedRunner := Runner{Home: t.TempDir(), Stdin: strings.NewReader(createdResp.Secret + "\n")}
	if out, err := run(scopedRunner, "auth", "use-key", "--server", serverAddr); err != nil {
		t.Fatalf("use-key: %v\n%s", err, out)
	}
	listText, err := run(human, "auth", "keys", "list")
	if err != nil {
		t.Fatalf("keys list: %v\n%s", err, listText)
	}
	for _, want := range []string{"alice/project", "contributor", "ci checks", "expires "} {
		if !strings.Contains(listText, want) {
			t.Fatalf("keys list output missing %q:\n%s", want, listText)
		}
	}
	if strings.Contains(listText, createdResp.Secret) {
		t.Fatalf("keys list leaked secret:\n%s", listText)
	}

	// Child key narrows to reader and inherits slice scope & parent_key_id.
	childOut, err := run(scopedRunner, "auth", "keys", "create", "--name", "child-read", "--role", "reader", "--json")
	if err != nil {
		t.Fatalf("child keys create: %v\n%s", err, childOut)
	}
	var childResp struct {
		Key    apiKeyOutput `json:"key"`
		Secret string       `json:"secret"`
	}
	if err := json.Unmarshal([]byte(childOut), &childResp); err != nil {
		t.Fatal(err)
	}
	if childResp.Key.ParentKeyID != createdResp.Key.ID || childResp.Key.MaxRole != "reader" ||
		len(childResp.Key.Slices) != 1 || childResp.Key.Slices[0] != "alice/project" {
		t.Fatalf("child key = %+v", childResp.Key)
	}
	// Attempting to widen beyond parent fails.
	if _, err := run(scopedRunner, "auth", "keys", "create", "--role", "writer"); err == nil {
		t.Fatal("expected child key widening role to fail")
	}
	// Revoking parent invalidates child too.
	if out, err := run(human, "auth", "keys", "revoke", createdResp.Key.ID); err != nil {
		t.Fatalf("keys revoke: %v\n%s", err, out)
	}
	childRunner := Runner{Home: t.TempDir()}
	if err := childRunner.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: childResp.Secret, SubjectID: humanID}); err != nil {
		t.Fatal(err)
	}
	if statusOut, err := run(childRunner, "auth", "status", "--json"); err != nil || !strings.Contains(statusOut, `"signed_in": false`) {
		t.Fatalf("child key must stop signing in after parent revoke: err=%v out=%s", err, statusOut)
	}
}
