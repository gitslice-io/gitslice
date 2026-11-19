package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestGetFolderPolicyAggregatesOwnersChecksTriggersSecretsAndRunners(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "user_bobby", "bobby")
	outsider := asUser(t, h, "user_outsider", "outsider")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "polorg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "polorg", Username: "bobby", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(bobby, &corev1.RespondToInvitationRequest{Account: "polorg", Accept: true}); err != nil {
		t.Fatal(err)
	}

	homeRef := &corev1.SliceRef{Account: "polorg", Slice: "home"}

	// Seed nested configs first, then root checks.yaml (whose required: true
	// check would otherwise gate subsequent commits in the same account).
	commitSliceFile(t, alice, h, mem, homeRef, "/polorg/.gitslice/owners.yaml", `version: 1
owners:
  - paths: ["**"]
    owners: ["alice"]
    require: 1
    require_human: 1
`)
	commitSliceFile(t, alice, h, mem, homeRef, "/polorg/web/.gitslice/checks.yaml", `version: 1
checks:
  build:
    run: npm run build
    paths: ["src/**"]
`)
	commitSliceFile(t, alice, h, mem, homeRef, "/polorg/web/.gitslice/automations.yaml", `version: 1
automations:
  deploy:
    on: [push, manual]
    action:
      http:
        url: https://hooks.example.com/deploy
`)
	commitSliceFile(t, alice, h, mem, homeRef, "/polorg/.gitslice/checks.yaml", `version: 1
checks:
  lint:
    run: go vet ./...
    required: true
`)

	// Create one account-root secret and one folder-scoped override + folder-only secret.
	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "polorg",
		Name:    "SHARED_KEY",
		Value:   "root-val",
		Scope:   &corev1.SecretScope{Paths: []string{"/polorg"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "polorg",
		Name:    "WEB_TOKEN",
		Value:   "web-val",
		Scope:   &corev1.SecretScope{Paths: []string{"/polorg/web"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "polorg",
		Name:    "OTHER_SECRET",
		Value:   "other-val",
		Scope:   &corev1.SecretScope{Paths: []string{"/polorg/api"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Register and attach an account CI runner.
	daemon, err := mem.Agents.RegisterDaemon(context.Background(), storage.AgentDaemonInput{
		SubjectID: "owner_alice",
		Account:   "polorg",
		Name:      "org-ci",
		Runtime:   "ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.SetAccountCIDaemon(alice, &corev1.SetAccountCIDaemonRequest{
		Account:  "polorg",
		DaemonId: daemon.Id,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Account admin sees full effective policy at /polorg/web/src.
	got, err := h.Trigger.GetFolderPolicy(alice, &corev1.GetFolderPolicyRequest{Path: "/polorg/web/src"})
	if err != nil {
		t.Fatalf("GetFolderPolicy(alice): %v", err)
	}
	if got.Folder != "/polorg/web/src" || got.Account != "polorg" || got.CommitId == "" {
		t.Fatalf("header = %+v", got)
	}
	if got.OwnerConfigPath != "/polorg/.gitslice/owners.yaml" || got.OwnerSourceFolder != "/polorg" {
		t.Fatalf("owners source = %q / %q", got.OwnerConfigPath, got.OwnerSourceFolder)
	}
	if len(got.OwnerRules) != 1 || got.OwnerRules[0].Require != 1 || got.OwnerRules[0].RequireHuman != 1 {
		t.Fatalf("owner rules = %+v", got.OwnerRules)
	}
	if len(got.Checks) != 2 || got.Checks[0].Id != "lint" || !got.Checks[0].Required || got.Checks[1].Id != "web/build" || got.Checks[1].Required {
		t.Fatalf("checks = %+v", got.Checks)
	}
	if len(got.Triggers) != 1 || got.Triggers[0].Id != "web/deploy" {
		t.Fatalf("triggers = %+v", got.Triggers)
	}
	if !got.CanManageSecrets {
		t.Fatal("expected CanManageSecrets=true for admin")
	}
	if len(got.Secrets) != 2 || got.Secrets[0].Name != "SHARED_KEY" || got.Secrets[0].SourceFolder != "/polorg" ||
		got.Secrets[1].Name != "WEB_TOKEN" || got.Secrets[1].SourceFolder != "/polorg/web" {
		t.Fatalf("secrets for admin = %+v", got.Secrets)
	}
	if len(got.CiDaemonIds) != 1 || got.CiDaemonIds[0] != daemon.Id || got.CiDaemonNames[0] != "org-ci" {
		t.Fatalf("ci daemons = %v / %v", got.CiDaemonIds, got.CiDaemonNames)
	}

	// 2. Account writer sees secret names ONLY (no IDs/paths) and CanManageSecrets=false.
	gotWriter, err := h.Trigger.GetFolderPolicy(bobby, &corev1.GetFolderPolicyRequest{Path: "/polorg/web/src"})
	if err != nil {
		t.Fatalf("GetFolderPolicy(bobby): %v", err)
	}
	if gotWriter.CanManageSecrets {
		t.Fatal("expected CanManageSecrets=false for writer")
	}
	if len(gotWriter.Secrets) != 2 || gotWriter.Secrets[0].SecretId != "" || gotWriter.Secrets[0].SourceFolder != "" || len(gotWriter.Secrets[0].Paths) != 0 {
		t.Fatalf("writer should see names only, got %+v", gotWriter.Secrets)
	}

	// 3. Unauthorized outsider is denied on private folder.
	_, err = h.Trigger.GetFolderPolicy(outsider, &corev1.GetFolderPolicyRequest{Path: "/polorg/web/src"})
	wantCode(t, err, codes.PermissionDenied, "outsider denied private folder policy")
}
