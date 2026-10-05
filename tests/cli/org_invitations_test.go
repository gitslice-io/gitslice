package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOrganizationInvitationsProfilesAndActiveAccount is the self-service
// flow end to end through gs: a user creates an organization, describes it,
// invites someone who accepts, and that person makes it their active account.
func TestOrganizationInvitationsProfilesAndActiveAccount(t *testing.T) {
	ts := startTestServer(t)
	ownerToken, _, ownerSubject := ts.provisionAccount(t, "inv-owner", "inv-owner")
	memberToken, _, memberSubject := ts.provisionAccount(t, "inv-member", "inv-member")
	ownerHome := t.TempDir()
	writeCLIAuthConfig(t, ownerHome, ts.addr, ownerToken, ownerSubject)
	memberHome := t.TempDir()
	writeCLIAuthConfig(t, memberHome, ts.addr, memberToken, memberSubject)
	cwd := t.TempDir()

	// Anyone with a username creates an organization; they own it.
	runCLI(t, ownerHome, cwd, "account", "create-org", "inv-labs")
	if out := runCLI(t, ownerHome, cwd, "account", "members", "inv-labs"); !strings.Contains(out, "inv-owner\towner") {
		t.Fatalf("members after creating:\n%s", out)
	}
	runCLI(t, ownerHome, cwd, "account", "set-profile", "inv-labs", "--name", "Inv Labs", "--website", "https://inv.example.com")
	runCLI(t, ownerHome, cwd, "account", "set-profile", "inv-labs", "--description", "Testing invitations.")
	profile := runCLI(t, memberHome, cwd, "account", "profile", "inv-labs", "--json")
	var got struct {
		Kind, DisplayName, Description, Website string
	}
	if err := json.Unmarshal([]byte(profile), &struct {
		Kind        *string `json:"kind"`
		DisplayName *string `json:"display_name"`
		Description *string `json:"description"`
		Website     *string `json:"website"`
	}{&got.Kind, &got.DisplayName, &got.Description, &got.Website}); err != nil {
		t.Fatalf("profile %q: %v", profile, err)
	}
	// Each set-profile keeps the fields it was not given.
	if got.Kind != "organization" || got.DisplayName != "Inv Labs" || got.Description != "Testing invitations." || got.Website != "https://inv.example.com" {
		t.Fatalf("profile = %+v", got)
	}

	// Adding someone directly is refused; they are invited and accept.
	if _, err := runCLIResult(ownerHome, cwd, "account", "set-member", "inv-labs", "inv-member", "--role", "writer"); err == nil || !strings.Contains(err.Error(), "invite") {
		t.Fatalf("set-member on a non-member should point at invite, got %v", err)
	}
	runCLI(t, ownerHome, cwd, "account", "invite", "inv-labs", "inv-member", "--role", "writer")
	if out := runCLI(t, ownerHome, cwd, "account", "invitations", "inv-labs"); !strings.Contains(out, "inv-member\twriter") {
		t.Fatalf("org invitations:\n%s", out)
	}
	if out := runCLI(t, memberHome, cwd, "account", "invitations"); !strings.Contains(out, "inv-labs\twriter\tinvited by inv-owner") {
		t.Fatalf("my invitations:\n%s", out)
	}
	if _, err := runCLIResult(memberHome, cwd, "account", "use", "inv-labs"); err == nil {
		t.Fatal("using an account before joining it should fail")
	}
	runCLI(t, memberHome, cwd, "account", "accept", "inv-labs")
	if out := runCLI(t, memberHome, cwd, "account", "invitations"); !strings.Contains(out, "no pending invitations") {
		t.Fatalf("invitations after accepting:\n%s", out)
	}

	// The active account becomes the default for commands.
	if out := runCLI(t, memberHome, cwd, "account", "current"); !strings.Contains(out, "inv-member (your personal account") {
		t.Fatalf("current before use:\n%s", out)
	}
	runCLI(t, memberHome, cwd, "account", "use", "inv-labs")
	if out := runCLI(t, memberHome, cwd, "account", "current"); strings.TrimSpace(out) != "inv-labs" {
		t.Fatalf("current after use:\n%s", out)
	}
	if out := runCLI(t, memberHome, cwd, "slice", "list"); !strings.Contains(out, "slices for account inv-labs") {
		t.Fatalf("slice list with an active organization:\n%s", out)
	}
	if out := runCLI(t, memberHome, cwd, "auth", "status", "--json"); !strings.Contains(out, `"active_account": "inv-labs"`) {
		t.Fatalf("auth status:\n%s", out)
	}
	runCLI(t, memberHome, cwd, "account", "use", "--clear")
	if out := runCLI(t, memberHome, cwd, "slice", "list"); !strings.Contains(out, "slices for account inv-member") {
		t.Fatalf("slice list after clearing:\n%s", out)
	}

	// Declining and cancelling.
	thirdToken, _, thirdSubject := ts.provisionAccount(t, "inv-third", "inv-third")
	thirdHome := t.TempDir()
	writeCLIAuthConfig(t, thirdHome, ts.addr, thirdToken, thirdSubject)
	runCLI(t, ownerHome, cwd, "account", "invite", "inv-labs", "inv-third")
	runCLI(t, thirdHome, cwd, "account", "decline", "inv-labs")
	if _, err := runCLIResult(thirdHome, cwd, "account", "accept", "inv-labs"); err == nil {
		t.Fatal("accepting a declined invitation should fail")
	}
	runCLI(t, ownerHome, cwd, "account", "invite", "inv-labs", "inv-third", "--role", "reader")
	runCLI(t, ownerHome, cwd, "account", "cancel-invite", "inv-labs", "inv-third")
	if out := runCLI(t, thirdHome, cwd, "account", "invitations"); !strings.Contains(out, "no pending invitations") {
		t.Fatalf("invitations after cancelling:\n%s", out)
	}
}
