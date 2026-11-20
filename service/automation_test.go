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
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/storage"
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
	engine, trigSvc, runSvc := newAutomationTestServices(mem, dispatchSecret)

	// 1. Writer (bobby) cannot invoke admin-only deploy-prod.
	if _, err := trigSvc.RunAutomation(bobby, &corev1.RunAutomationRequest{
		Account: "autoorg", AutomationId: "infra/deploy-prod", Inputs: map[string]string{"version": "v1.0"},
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("writer RunAutomation(admin-only) = %v, want PermissionDenied", err)
	}

	// 2. Agent admin cannot invoke require_human deploy-prod.
	if _, err := trigSvc.RunAutomation(botCtx, &corev1.RunAutomationRequest{
		Account: "autoorg", AutomationId: "infra/deploy-prod", Inputs: map[string]string{"version": "v1.0"},
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("agent RunAutomation(require_human) = %v, want PermissionDenied", err)
	}

	// 3. Agent CAN invoke deploy-staging (capability-based secret execution without require_human).
	stageRun, err := trigSvc.RunAutomation(botCtx, &corev1.RunAutomationRequest{
		Account: "autoorg", AutomationId: "infra/deploy-staging",
	})
	if err != nil {
		t.Fatalf("agent RunAutomation(deploy-staging): %v", err)
	}
	if stageRun.GetTriggeredByKind() != "agent" {
		t.Fatalf("stageRun triggered_by_kind = %q, want agent", stageRun.GetTriggeredByKind())
	}

	// 4. Human admin (alice) gets validation error when required input is missing.
	if _, err := trigSvc.RunAutomation(alice, &corev1.RunAutomationRequest{
		Account: "autoorg", AutomationId: "infra/deploy-prod",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing input err = %v, want InvalidArgument", err)
	}

	// 5. Human admin (alice) succeeds with valid inputs; defaults and attribution populated.
	prodRun, err := trigSvc.RunAutomation(alice, &corev1.RunAutomationRequest{
		Account: "autoorg", AutomationId: "infra/deploy-prod", Inputs: map[string]string{"version": "v2.1"},
	})
	if err != nil {
		t.Fatalf("alice RunAutomation(deploy-prod): %v", err)
	}
	if prodRun.GetTriggeredBy() != "alice" || prodRun.GetTriggeredByKind() != "human" {
		t.Fatalf("attribution = (%q, %q), want (alice, human)", prodRun.GetTriggeredBy(), prodRun.GetTriggeredByKind())
	}
	if prodRun.GetInputs()["version"] != "v2.1" || prodRun.GetInputs()["dry_run"] != "false" {
		t.Fatalf("inputs = %+v", prodRun.GetInputs())
	}

	// 6. StartRun returns bash script, runtime, egress, secrets, inputs, and run token.
	started, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{
		RunId: prodRun.GetId(), DispatcherKey: dispatchSecret,
	})
	if err != nil {
		t.Fatalf("StartRun(bash): %v", err)
	}
	if started.GetAction() != automations.ActionBash || started.GetEgress() != automations.EgressHTTPGate {
		t.Fatalf("started action/egress = %q/%q", started.GetAction(), started.GetEgress())
	}
	if started.GetBashRuntime() != automations.BashRuntimeCloud || started.GetBashScript() != "#!/usr/bin/env bash\necho deployed\n" {
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
	if len(policy.GetAutomations()) != 2 {
		t.Fatalf("policy triggers = %d, want 2", len(policy.GetAutomations()))
	}
	if policy.GetAutomations()[0].GetManual() == nil || !policy.GetAutomations()[0].GetManual().GetRequireHuman() {
		t.Fatalf("policy manual = %+v", policy.GetAutomations()[0].GetManual())
	}
}

func TestSharedSliceMemberAutomationListRunFollowAndIsolation(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	secReader := asUser(t, h, "user_secreader", "secreader")
	stranger := asUser(t, h, "user_stranger", "stranger")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "opsorg"}); err != nil {
		t.Fatal(err)
	}

	// Register an agent that is NOT a member of opsorg, only shared on opsorg:app as writer.
	botReg, err := mem.Auth.RegisterAgent(context.Background(), storage.RegisterAgentInput{
		Username: "ops-bot", OwnerEmail: "alice@example.com", DisplayName: "Ops Bot",
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	bot := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID: botReg.SubjectID, SubjectKind: "agent",
	})

	homeRef := &corev1.SliceRef{Account: "opsorg", Slice: "home"}
	commitSliceFile(t, alice, h, mem, homeRef, "/opsorg/app/.gitslice/automations.yaml", `version: 1
automations:
  selftest:
    on: [manual]
    manual:
      roles: [admin, writer]
    action:
      http:
        url: https://hooks.example.com/selftest
`)
	commitSliceFile(t, alice, h, mem, homeRef, "/opsorg/secret/.gitslice/automations.yaml", `version: 1
automations:
  secret-op:
    on: [manual]
    manual:
      roles: [admin, writer]
    action:
      http:
        url: https://hooks.example.com/secret
`)

	appRef := &corev1.SliceRef{Account: "opsorg", Slice: "app"}
	secretRef := &corev1.SliceRef{Account: "opsorg", Slice: "secret"}
	if _, err := h.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:           appRef,
		IncludedPaths: []string{"/opsorg/app"},
		Visibility:    "public",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:           secretRef,
		IncludedPaths: []string{"/opsorg/secret"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice: appRef, Username: "ops-bot", Role: "writer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice: secretRef, Username: "secreader", Role: "reader",
	}); err != nil {
		t.Fatal(err)
	}

	_, trigSvc, _ := newAutomationTestServices(mem, "test-dispatch-secret")

	// Alice starts a run in the private secret folder.
	secretRun, err := trigSvc.RunAutomation(alice, &corev1.RunAutomationRequest{
		Account: "opsorg", AutomationId: "secret/secret-op",
	})
	if err != nil {
		t.Fatalf("alice RunAutomation(secret/secret-op): %v", err)
	}
	if _, err := mem.Automations.AppendAutomationRunLog(context.Background(), secretRun.GetId(), "stdout", "secret output\n"); err != nil {
		t.Fatal(err)
	}

	// 1. Shared slice writer (bot) can list, start, inspect, read logs, list runs, cancel, and rerun on /opsorg/app.
	listed, err := trigSvc.ListAutomations(bot, &corev1.ListAutomationsRequest{Path: "/opsorg/app"})
	if err != nil || len(listed.GetAutomations()) != 1 || listed.GetAutomations()[0].GetId() != "app/selftest" {
		t.Fatalf("bot ListAutomations(/opsorg/app) = %+v, %v", listed, err)
	}
	if _, err := trigSvc.ListAutomations(bot, &corev1.ListAutomationsRequest{Path: "/opsorg/secret"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bot ListAutomations(/opsorg/secret) = %v, want PermissionDenied", err)
	}

	appRun, err := trigSvc.RunAutomation(bot, &corev1.RunAutomationRequest{
		Account: "opsorg", AutomationId: "app/selftest",
	})
	if err != nil {
		t.Fatalf("bot RunAutomation(app/selftest): %v", err)
	}
	if _, err := mem.Automations.AppendAutomationRunLog(context.Background(), appRun.GetId(), "stdout", "selftest ok\n"); err != nil {
		t.Fatal(err)
	}

	if got, err := trigSvc.GetAutomationRun(bot, &corev1.GetAutomationRunRequest{RunId: appRun.GetId()}); err != nil || got.GetId() != appRun.GetId() {
		t.Fatalf("bot GetAutomationRun(appRun) = %+v, %v", got, err)
	}
	if logs, err := trigSvc.ListAutomationRunLogs(bot, &corev1.ListAutomationRunLogsRequest{RunId: appRun.GetId()}); err != nil || len(logs.GetLogs()) != 1 {
		t.Fatalf("bot ListAutomationRunLogs(appRun) = %+v, %v", logs, err)
	}
	// ListAutomationRuns for opsorg returns ONLY app runs, never secret runs.
	runsResp, err := trigSvc.ListAutomationRuns(bot, &corev1.ListAutomationRunsRequest{Account: "opsorg"})
	if err != nil {
		t.Fatalf("bot ListAutomationRuns(opsorg): %v", err)
	}
	if len(runsResp.GetRuns()) != 1 || runsResp.GetRuns()[0].GetId() != appRun.GetId() {
		t.Fatalf("bot ListAutomationRuns(opsorg) = %+v, want only appRun", runsResp.GetRuns())
	}
	if byTrigger, err := trigSvc.ListAutomationRuns(bot, &corev1.ListAutomationRunsRequest{Account: "opsorg", AutomationId: "app/selftest"}); err != nil || len(byTrigger.GetRuns()) != 1 {
		t.Fatalf("bot ListAutomationRuns(app/selftest) = %+v, %v", byTrigger, err)
	}
	if _, err := trigSvc.ListAutomationRuns(bot, &corev1.ListAutomationRunsRequest{Account: "opsorg", AutomationId: "secret/secret-op"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bot ListAutomationRuns(secret/secret-op) = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.GetAutomationRun(bot, &corev1.GetAutomationRunRequest{RunId: secretRun.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bot GetAutomationRun(secretRun) = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.CancelAutomationRun(bot, &corev1.CancelAutomationRunRequest{RunId: appRun.GetId()}); err != nil {
		t.Fatalf("bot CancelAutomationRun(appRun): %v", err)
	}
	if _, err := trigSvc.RerunAutomationRun(bot, &corev1.RerunAutomationRunRequest{RunId: appRun.GetId()}); err != nil {
		t.Fatalf("bot RerunAutomationRun(appRun): %v", err)
	}

	// 2. Unshared stranger on the public slice CANNOT read/list/cancel/rerun manual runs on /opsorg/app.
	if _, err := trigSvc.GetAutomationRun(stranger, &corev1.GetAutomationRunRequest{RunId: appRun.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stranger GetAutomationRun(appRun) = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.ListAutomationRunLogs(stranger, &corev1.ListAutomationRunLogsRequest{RunId: appRun.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stranger ListAutomationRunLogs(appRun) = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.ListAutomationRuns(stranger, &corev1.ListAutomationRunsRequest{Account: "opsorg"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stranger ListAutomationRuns(opsorg) = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.ListAutomationRuns(stranger, &corev1.ListAutomationRunsRequest{Account: "opsorg", AutomationId: "app/selftest"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stranger ListAutomationRuns(app/selftest) = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.CancelAutomationRun(stranger, &corev1.CancelAutomationRunRequest{RunId: appRun.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stranger CancelAutomationRun(appRun) = %v, want PermissionDenied", err)
	}

	// 3. Shared slice reader on private opsorg:secret can read runs/logs there, but cannot run/cancel/rerun.
	if _, err := trigSvc.ListAutomations(secReader, &corev1.ListAutomationsRequest{Path: "/opsorg/secret"}); err != nil {
		t.Fatalf("secReader ListAutomations(/opsorg/secret): %v", err)
	}
	if _, err := trigSvc.GetAutomationRun(secReader, &corev1.GetAutomationRunRequest{RunId: secretRun.GetId()}); err != nil {
		t.Fatalf("secReader GetAutomationRun(secretRun): %v", err)
	}
	if _, err := trigSvc.ListAutomationRunLogs(secReader, &corev1.ListAutomationRunLogsRequest{RunId: secretRun.GetId()}); err != nil {
		t.Fatalf("secReader ListAutomationRunLogs(secretRun): %v", err)
	}
	if secRuns, err := trigSvc.ListAutomationRuns(secReader, &corev1.ListAutomationRunsRequest{Account: "opsorg"}); err != nil || len(secRuns.GetRuns()) != 1 || secRuns.GetRuns()[0].GetId() != secretRun.GetId() {
		t.Fatalf("secReader ListAutomationRuns(opsorg) = %+v, %v", secRuns, err)
	}
	if _, err := trigSvc.RunAutomation(secReader, &corev1.RunAutomationRequest{Account: "opsorg", AutomationId: "secret/secret-op"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("secReader RunAutomation(secret-op) = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.CancelAutomationRun(secReader, &corev1.CancelAutomationRunRequest{RunId: secretRun.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("secReader CancelAutomationRun(secretRun) = %v, want PermissionDenied", err)
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
