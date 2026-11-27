package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/storage"
)

func TestGroupAndApproversCLI(t *testing.T) {
	ctx := context.Background()
	const aliceToken = "alice-token"
	staticTokens := map[string]string{}
	serverAddr, mem := startAgentSignupServerWithStores(t, false, staticTokens)
	aliceID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "alice_ext", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	staticTokens[aliceToken] = aliceID
	if _, err := mem.Auth.ChooseUsername(ctx, aliceID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Auth.CreateOrganization(ctx, "acme", []string{aliceID}, aliceID); err != nil {
		t.Fatal(err)
	}
	bobID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "bob_ext", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, bobID, "bobby"); err != nil {
		t.Fatal(err)
	}

	data := []byte("package api\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(objectstore.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_cli_grp", []storage.FileEntry{{
		Path:        "/acme/api/main.go",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/api/main.go"})

	alice := Runner{Home: t.TempDir()}
	if err := alice.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: aliceToken, SubjectID: aliceID}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		r := alice
		r.Stdout, r.Stderr = &stdout, &stderr
		err := r.Run(ctx, args)
		if err != nil {
			return stdout.String() + stderr.String(), err
		}
		return stdout.String(), nil
	}

	out, err := run("group", "create", "@acme/api", "--description", "API reviewers", "--json")
	if err != nil {
		t.Fatalf("group create: %v\n%s", err, out)
	}
	var grp groupOutput
	if err := json.Unmarshal([]byte(out), &grp); err != nil || grp.Handle != "@acme/api" || grp.Description != "API reviewers" {
		t.Fatalf("group create JSON = %q, %v", out, err)
	}

	if out, err := run("group", "add", "@acme/api", "bobby"); err != nil || !strings.Contains(out, "added bobby to @acme/api") {
		t.Fatalf("group add: %v\n%s", err, out)
	}
	if out, err := run("group", "show", "@acme/api"); err != nil || !strings.Contains(out, "@acme/api") || !strings.Contains(out, "bobby") {
		t.Fatalf("group show: %v\n%s", err, out)
	}
	if out, err := run("group", "list", "acme"); err != nil || !strings.Contains(out, "@acme/api\t1 member") {
		t.Fatalf("group list: %v\n%s", err, out)
	}

	out, err = run("slice", "create", "acme:gated", "--include", "/acme/api", "--required-approvals", "1", "--approvers", "@acme/api", "--json")
	if err != nil {
		t.Fatalf("slice create --approvers: %v\n%s", err, out)
	}
	var sl sliceOutput
	if err := json.Unmarshal([]byte(out), &sl); err != nil || sl.Approvers != "@acme/api" || sl.RequiredApprovals != 1 {
		t.Fatalf("slice create JSON = %q, %v", out, err)
	}

	if out, err := run("slice", "share", "acme:gated", "@acme/api", "--role", "contributor"); err != nil || !strings.Contains(out, "shared acme/gated with @acme/api as contributor") {
		t.Fatalf("slice share @group: %v\n%s", err, out)
	}
	out, err = run("slice", "members", "acme:gated", "--json")
	if err != nil {
		t.Fatalf("slice members: %v\n%s", err, out)
	}
	var memOut sliceMembersOutput
	if err := json.Unmarshal([]byte(out), &memOut); err != nil || len(memOut.GroupMembers) != 1 || memOut.GroupMembers[0].Handle != "@acme/api" {
		t.Fatalf("slice members JSON = %q, %v", out, err)
	}
	if out, err := run("slice", "revoke", "acme:gated", "@acme/api"); err != nil || !strings.Contains(out, "@acme/api no longer has a role on acme/gated") {
		t.Fatalf("slice revoke @group: %v\n%s", err, out)
	}

	// Clear --approvers with empty string.
	out, err = run("slice", "update", "acme:gated", "--approvers", "", "--json")
	if err != nil {
		t.Fatalf("slice update --approvers '': %v\n%s", err, out)
	}
	var cleared sliceOutput
	if err := json.Unmarshal([]byte(out), &cleared); err != nil || cleared.Approvers != "" || cleared.Version != 2 {
		t.Fatalf("slice update clear approvers JSON = %q, %v", out, err)
	}

	if out, err := run("group", "rm", "@acme/api", "bobby"); err != nil || !strings.Contains(out, "removed bobby from @acme/api") {
		t.Fatalf("group rm: %v\n%s", err, out)
	}
	if out, err := run("group", "delete", "@acme/api"); err != nil || !strings.Contains(out, "deleted group @acme/api") {
		t.Fatalf("group delete: %v\n%s", err, out)
	}
}
