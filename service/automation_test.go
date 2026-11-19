package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestManualAutomationRBACInputsAndBashStartAndArchive(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "user_bobby", "bobby")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "autoorg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "autoorg", Username: "bobby", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(bobby, &corev1.RespondToInvitationRequest{Account: "autoorg", Accept: true}); err != nil {
		t.Fatal(err)
	}

	// Register an agent and invite it as an admin of autoorg.
	agentReg, err := mem.Auth.RegisterAgent(context.Background(), storage.RegisterAgentInput{
		Username: "deploy-bot", OwnerEmail: "alice@example.com", DisplayName: "Deploy Bot",
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	botCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID: agentReg.SubjectID, SubjectKind: "agent",
	})
	if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "autoorg", Username: "deploy-bot", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(botCtx, &corev1.RespondToInvitationRequest{Account: "autoorg", Accept: true}); err != nil {
		t.Fatal(err)
	}

	homeRef := &corev1.SliceRef{Account: "autoorg", Slice: "home"}
	commitSliceFile(t, alice, h, mem, homeRef, "/autoorg/shared/helper.sh", "echo shared\n")
	commitSliceFile(t, alice, h, mem, homeRef, "/autoorg/infra/app.txt", "infra-data\n")
	commitSliceFile(t, alice, h, mem, homeRef, "/autoorg/infra/.gitslice/scripts/deploy.sh", "#!/usr/bin/env bash\necho deployed\n")
	commitSliceFile(t, alice, h, mem, homeRef, "/autoorg/infra/.gitslice/automations.yaml", `version: 1
automations:
  deploy-prod:
    description: Production deploy
    on: [manual]
    manual:
      roles: [admin]
      require_human: true
      confirm: "Deploy to prod?"
    inputs:
      version:
        type: string
        required: true
      dry_run:
        type: boolean
        default: false
    secrets: [DEPLOY_KEY]
    network: [api.example.com]
    action:
      bash:
        runtime: cloud
        size: basic
        checkout: folder
        include: [/autoorg/shared]
        script: scripts/deploy.sh
  deploy-staging:
    on: [manual]
    secrets: [DEPLOY_KEY]
    network: [api.example.com]
    action:
      bash:
        checkout: none
        run: echo staging
`)

	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "autoorg",
		Name:    "DEPLOY_KEY",
		Value:   "super-secret-value",
		Scope:   &corev1.SecretScope{Paths: []string{"/autoorg/infra"}},
	}); err != nil {
		t.Fatal(err)
	}

	dispatchSecret := "test-dispatch-secret"
	engine, trigSvc, runSvc := newTriggerTestServices(mem, dispatchSecret)

	// 1. Writer (bobby) cannot invoke admin-only deploy-prod.
	if _, err := trigSvc.RunTrigger(bobby, &corev1.RunTriggerRequest{
		Account: "autoorg", TriggerId: "infra/deploy-prod", Inputs: map[string]string{"version": "v1.0"},
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("writer RunTrigger(admin-only) = %v, want PermissionDenied", err)
	}

	// 2. Agent admin cannot invoke require_human deploy-prod.
	if _, err := trigSvc.RunTrigger(botCtx, &corev1.RunTriggerRequest{
		Account: "autoorg", TriggerId: "infra/deploy-prod", Inputs: map[string]string{"version": "v1.0"},
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("agent RunTrigger(require_human) = %v, want PermissionDenied", err)
	}

	// 3. Agent CAN invoke deploy-staging (capability-based secret execution without require_human).
	stageRun, err := trigSvc.RunTrigger(botCtx, &corev1.RunTriggerRequest{
		Account: "autoorg", TriggerId: "infra/deploy-staging",
	})
	if err != nil {
		t.Fatalf("agent RunTrigger(deploy-staging): %v", err)
	}
	if stageRun.GetTriggeredByKind() != "agent" {
		t.Fatalf("stageRun triggered_by_kind = %q, want agent", stageRun.GetTriggeredByKind())
	}

	// 4. Human admin (alice) gets validation error when required input is missing.
	if _, err := trigSvc.RunTrigger(alice, &corev1.RunTriggerRequest{
		Account: "autoorg", TriggerId: "infra/deploy-prod",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing input err = %v, want InvalidArgument", err)
	}

	// 5. Human admin (alice) succeeds with valid inputs; defaults and attribution populated.
	prodRun, err := trigSvc.RunTrigger(alice, &corev1.RunTriggerRequest{
		Account: "autoorg", TriggerId: "infra/deploy-prod", Inputs: map[string]string{"version": "v2.1"},
	})
	if err != nil {
		t.Fatalf("alice RunTrigger(deploy-prod): %v", err)
	}
	if prodRun.GetTriggeredBy() != "alice" || prodRun.GetTriggeredByKind() != "human" {
		t.Fatalf("attribution = (%q, %q), want (alice, human)", prodRun.GetTriggeredBy(), prodRun.GetTriggeredByKind())
	}
	if prodRun.GetInputs()["version"] != "v2.1" || prodRun.GetInputs()["dry_run"] != "false" {
		t.Fatalf("inputs = %+v", prodRun.GetInputs())
	}

	// 6. StartRun returns bash script, runtime, egress, secrets, inputs, and run token.
	started, err := runSvc.StartRun(context.Background(), &corev1.StartTriggerRunRequest{
		RunId: prodRun.GetId(), DispatcherKey: dispatchSecret,
	})
	if err != nil {
		t.Fatalf("StartRun(bash): %v", err)
	}
	if started.GetAction() != triggers.ActionBash || started.GetEgress() != triggers.EgressHTTPGate {
		t.Fatalf("started action/egress = %q/%q", started.GetAction(), started.GetEgress())
	}
	if started.GetBashRuntime() != triggers.BashRuntimeCloud || started.GetBashScript() != "#!/usr/bin/env bash\necho deployed\n" {
		t.Fatalf("started bash = runtime=%q script=%q", started.GetBashRuntime(), started.GetBashScript())
	}
	if started.GetSecrets()["DEPLOY_KEY"] != "super-secret-value" {
		t.Fatalf("missing secret in StartRun")
	}

	// 7. ServeArchiveHTTP streams folder + include files when authenticated with run token.
	req := httptest.NewRequest(http.MethodGet, "/automation-archive", nil)
	req.Header.Set("Authorization", "Bearer "+started.GetRunToken())
	rec := httptest.NewRecorder()
	runSvc.ServeArchiveHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", rec.Code, rec.Body.String())
	}
	files := readTarGz(t, rec.Body)
	if files["infra/app.txt"] != "infra-data\n" || files["shared/helper.sh"] != "echo shared\n" {
		t.Fatalf("archive files = %+v", files)
	}

	// Unauthenticated archive request rejected.
	badReq := httptest.NewRequest(http.MethodGet, "/automation-archive", nil)
	badRec := httptest.NewRecorder()
	runSvc.ServeArchiveHTTP(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth archive status = %d, want 401", badRec.Code)
	}

	// Keep engine reference used (also verifies folder policy surfaces new metadata).
	_ = engine
	policy, err := trigSvc.GetFolderPolicy(alice, &corev1.GetFolderPolicyRequest{Path: "/autoorg/infra"})
	if err != nil {
		t.Fatalf("GetFolderPolicy: %v", err)
	}
	if len(policy.GetTriggers()) != 2 {
		t.Fatalf("policy triggers = %d, want 2", len(policy.GetTriggers()))
	}
	if policy.GetTriggers()[0].GetManual() == nil || !policy.GetTriggers()[0].GetManual().GetRequireHuman() {
		t.Fatalf("policy manual = %+v", policy.GetTriggers()[0].GetManual())
	}
}

func readTarGz(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(data)
	}
}
