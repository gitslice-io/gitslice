package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReusableTemplateCrossAccountTagAndCapabilities(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "owner_bobby", "bobby")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "stdorg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.CreateOrganization(bobby, &corev1.CreateOrganizationRequest{Slug: "apporg"}); err != nil {
		t.Fatal(err)
	}

	// stdorg publishes a public reusable template under /stdorg/cf and tags it v1.0.0.
	stdHome := &corev1.SliceRef{Account: "stdorg", Slice: "home"}
	commitSliceFile(t, alice, h, mem, stdHome, "/stdorg/cf/.gitslice/deploy.sh", "#!/usr/bin/env bash\necho deploying $INPUT_DIR $INPUT_ENV\n")
	commitSliceFile(t, alice, h, mem, stdHome, "/stdorg/cf/.gitslice/automations.yaml", `version: 1
automations:
  deploy-worker:
    description: Deploy a Cloudflare Worker
    on: [call]
    inputs:
      dir:
        type: string
        default: "."
      env:
        type: choice
        options: [staging, prod]
        required: true
    secrets: [CLOUDFLARE_API_TOKEN]
    network: [api.cloudflare.com]
    timeout: 10m
    action:
      bash:
        runtime: node
        size: lite
        checkout: folder
        script: deploy.sh
`)
	stdSliceRef := &corev1.SliceRef{Account: "stdorg", Slice: "cf"}
	if _, err := h.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:           stdSliceRef,
		IncludedPaths: []string{"/stdorg/cf"},
		Visibility:    "public",
	}); err != nil {
		t.Fatal(err)
	}
	tagResp, err := h.Slice.CreateTag(alice, &corev1.CreateTagRequest{
		Slice: stdSliceRef,
		Name:  "v1.0.0",
	})
	if err != nil {
		t.Fatalf("CreateTag: %v", err)
	}

	// apporg calls /stdorg/cf/deploy-worker@v1.0.0 with remapped secret APP_CF_TOKEN -> CLOUDFLARE_API_TOKEN.
	appHome := &corev1.SliceRef{Account: "apporg", Slice: "home"}
	commitSliceFile(t, bobby, h, mem, appHome, "/apporg/billing/worker.ts", "export default {}\n")
	commitSliceFile(t, bobby, h, mem, appHome, "/apporg/billing/.gitslice/automations.yaml", `version: 1
automations:
  deploy:
    description: Deploy billing worker
    on: [manual]
    secrets:
      CLOUDFLARE_API_TOKEN: APP_CF_TOKEN
    network: [api.cloudflare.com]
    action:
      uses: /stdorg/cf/deploy-worker@v1.0.0
      with:
        dir: worker
`)
	if _, err := h.Secret.CreateSecret(bobby, &corev1.CreateSecretRequest{
		Account: "apporg",
		Name:    "APP_CF_TOKEN",
		Value:   "cf-super-secret-123",
		Scope:   &corev1.SecretScope{Paths: []string{"/apporg/billing"}},
	}); err != nil {
		t.Fatal(err)
	}

	dispatchSecret := "test-dispatch-secret"
	_, trigSvc, runSvc := newAutomationTestServices(mem, dispatchSecret)

	// 1. InspectAutomationTemplate returns template metadata, commit, and available tags.
	inspected, err := trigSvc.InspectAutomationTemplate(bobby, &corev1.InspectAutomationTemplateRequest{
		Ref:           "/stdorg/cf/deploy-worker@v1.0.0",
		CallerAccount: "apporg",
	})
	if err != nil {
		t.Fatalf("InspectAutomationTemplate: %v", err)
	}
	if inspected.GetCommitId() != tagResp.GetCommitId() || len(inspected.GetAvailableTags()) != 1 || inspected.GetAvailableTags()[0] != "v1.0.0" {
		t.Fatalf("inspected = %+v", inspected)
	}
	if inspected.GetTemplate().GetAction() != automations.ActionBash || len(inspected.GetTemplate().GetInputs()) != 2 {
		t.Fatalf("inspected template = %+v", inspected.GetTemplate())
	}

	// 2. ListAutomations on /apporg/billing inherits unbound template input "env" (and omits fixed "dir").
	listed, err := trigSvc.ListAutomations(bobby, &corev1.ListAutomationsRequest{Path: "/apporg/billing"})
	if err != nil || len(listed.GetAutomations()) != 1 {
		t.Fatalf("ListAutomations = %+v, %v", listed, err)
	}
	autoDef := listed.GetAutomations()[0]
	if autoDef.GetUses().GetRef() != "/stdorg/cf/deploy-worker@v1.0.0" {
		t.Fatalf("uses = %+v", autoDef.GetUses())
	}
	if len(autoDef.GetInputs()) != 1 || autoDef.GetInputs()[0].GetName() != "env" {
		t.Fatalf("inherited inputs = %+v, want [env]", autoDef.GetInputs())
	}

	// 3. Running without required inherited input "env" fails with InvalidArgument.
	if _, err := trigSvc.RunAutomation(bobby, &corev1.RunAutomationRequest{
		Account: "apporg", AutomationId: "billing/deploy",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing inherited input err = %v, want InvalidArgument", err)
	}

	// 4. Running with env=prod starts a run linked to the template commit; StartRun loads deploy.sh
	// from stdorg's tagged tree while binding apporg's APP_CF_TOKEN under slot CLOUDFLARE_API_TOKEN.
	run, err := trigSvc.RunAutomation(bobby, &corev1.RunAutomationRequest{
		Account: "apporg", AutomationId: "billing/deploy", Inputs: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("RunAutomation: %v", err)
	}
	if run.GetTemplateId() != "/stdorg/cf/deploy-worker" || run.GetTemplateCommitId() != tagResp.GetCommitId() {
		t.Fatalf("template provenance = (%q, %q)", run.GetTemplateId(), run.GetTemplateCommitId())
	}
	started, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{
		RunId: run.GetId(), DispatcherKey: dispatchSecret,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if started.GetBashScript() != "#!/usr/bin/env bash\necho deploying $INPUT_DIR $INPUT_ENV\n" {
		t.Fatalf("bash script = %q", started.GetBashScript())
	}
	if started.GetSecrets()["CLOUDFLARE_API_TOKEN"] != "cf-super-secret-123" {
		t.Fatalf("remapped secret = %+v", started.GetSecrets())
	}
	if started.GetInputs()["dir"] != "worker" || started.GetInputs()["env"] != "prod" {
		t.Fatalf("resolved inputs = %+v", started.GetInputs())
	}

	// Archive endpoint still checks out caller's folder (/apporg/billing), NOT stdorg!
	req := httptest.NewRequest(http.MethodGet, "/automation-archive", nil)
	req.Header.Set("Authorization", "Bearer "+started.GetRunToken())
	rec := httptest.NewRecorder()
	runSvc.ServeArchiveHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", rec.Code, rec.Body.String())
	}
	files := readTarGz(t, rec.Body)
	if files["billing/worker.ts"] != "export default {}\n" {
		t.Fatalf("archive files = %+v", files)
	}
}

func TestMultiStepPipelineOutputsConditionalsAndCancel(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "pipeorg"}); err != nil {
		t.Fatal(err)
	}
	home := &corev1.SliceRef{Account: "pipeorg", Slice: "home"}
	commitSliceFile(t, alice, h, mem, home, "/pipeorg/lib/.gitslice/automations.yaml", `version: 1
automations:
  build-step:
    on: [call]
    inputs:
      target:
        type: string
        required: true
    action:
      bash:
        runtime: base
        checkout: none
        run: echo build $INPUT_TARGET
`)
	commitSliceFile(t, alice, h, mem, home, "/pipeorg/svc/.gitslice/automations.yaml", `version: 1
automations:
  release:
    on: [manual]
    secrets: [DEPLOY_SECRET]
    inputs:
      target:
        type: string
        default: api
    action:
      steps:
        - id: build
          name: Build artifact
          uses: /pipeorg/lib/build-step
          with:
            target: "${{ inputs.target }}"
        - id: deploy
          name: Deploy artifact
          bash:
            runtime: base
            checkout: none
            env:
              ARTIFACT: "${{ steps.build.outputs.artifact }}"
            run: echo deploy $ARTIFACT
        - id: after_ok
          if: success
          bash:
            runtime: base
            checkout: none
            run: echo should-skip
        - id: cleanup
          if: failure
          bash:
            runtime: base
            checkout: none
            run: echo cleanup-ran
`)
	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "pipeorg",
		Name:    "DEPLOY_SECRET",
		Value:   "top-secret-xyz",
		Scope:   &corev1.SecretScope{Paths: []string{"/pipeorg/svc"}},
	}); err != nil {
		t.Fatal(err)
	}

	dispatchSecret := "test-dispatch-secret"
	// Mock dispatcher endpoint so Engine.post succeeds cleanly.
	dispSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer dispSrv.Close()

	tree := TreeFiles{Repository: mem.Repository, Objects: mem.Objects}
	engine := automations.NewEngine(automations.Stores{
		Automations: mem.Automations, Changesets: mem.Changesets, Slices: mem.Slices,
		Repository: mem.Repository, Checks: mem.Checks,
	}, automations.Options{
		Tree: tree, DispatchURL: dispSrv.URL, DispatchSecret: dispatchSecret,
		AllowPrivateTargets: true, DispatchClient: dispSrv.Client(),
		Templates: &sliceTemplateLookup{Repository: mem.Repository, Slices: mem.Slices, Auth: mem.Auth},
	})
	trigSvc := &AutomationService{
		Auth: mem.Auth, Changesets: mem.Changesets, Slices: mem.Slices, Repository: mem.Repository,
		Automations: mem.Automations, Tree: tree, Engine: engine,
	}
	runSvc := &AutomationRunService{
		Changesets: mem.Changesets, Slices: mem.Slices, Repository: mem.Repository, Checks: mem.Checks,
		Automations: mem.Automations, Tree: tree, Engine: engine,
	}

	// Start pipeline run and drain to launch step 1 (build).
	parent, err := trigSvc.RunAutomation(alice, &corev1.RunAutomationRequest{
		Account: "pipeorg", AutomationId: "svc/release",
	})
	if err != nil {
		t.Fatalf("RunAutomation(release): %v", err)
	}
	engine.Drain(context.Background())

	children, err := trigSvc.ListAutomationRuns(alice, &corev1.ListAutomationRunsRequest{ParentRunId: parent.GetId()})
	if err != nil || len(children.GetRuns()) != 1 || children.GetRuns()[0].GetStepId() != "build" {
		t.Fatalf("step 1 children = %+v, %v", children, err)
	}
	step1 := children.GetRuns()[0]
	if step1.GetTemplateId() != "/pipeorg/lib/build-step" {
		t.Fatalf("step 1 template_id = %q", step1.GetTemplateId())
	}

	// Start step 1, emit output (with secret to verify masking!), and finish step 1 as succeeded.
	s1Start, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: step1.GetId(), DispatcherKey: dispatchSecret})
	if err != nil {
		t.Fatalf("StartRun step1: %v", err)
	}
	if _, err := runSvc.SetRunOutput(context.Background(), &corev1.SetAutomationRunOutputRequest{
		RunToken: s1Start.GetRunToken(), Key: "artifact", Value: "sha256:999-top-secret-xyz",
	}); err != nil {
		t.Fatalf("SetRunOutput: %v", err)
	}
	if _, err := runSvc.FinishRun(context.Background(), &corev1.FinishAutomationRunRequest{
		RunToken: s1Start.GetRunToken(), Outcome: "succeeded",
	}); err != nil {
		t.Fatalf("FinishRun step1: %v", err)
	}

	// Step 2 (deploy) should now be queued with interpolated ARTIFACT env (secret masked!).
	children, _ = trigSvc.ListAutomationRuns(alice, &corev1.ListAutomationRunsRequest{ParentRunId: parent.GetId()})
	if len(children.GetRuns()) != 2 || children.GetRuns()[1].GetStepId() != "deploy" {
		t.Fatalf("step 2 children = %+v", children.GetRuns())
	}
	step2 := children.GetRuns()[1]
	s2Start, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: step2.GetId(), DispatcherKey: dispatchSecret})
	if err != nil {
		t.Fatalf("StartRun step2: %v", err)
	}
	if s2Start.GetBashEnv()["ARTIFACT"] != "sha256:999-***" {
		t.Fatalf("step 2 ARTIFACT = %q, want sha256:999-***", s2Start.GetBashEnv()["ARTIFACT"])
	}

	// Fail step 2 -> step 3 (if: success) should be skipped and step 4 (if: failure) should launch!
	if _, err := runSvc.FinishRun(context.Background(), &corev1.FinishAutomationRunRequest{
		RunToken: s2Start.GetRunToken(), Outcome: "failed", Error: "deploy failed",
	}); err != nil {
		t.Fatalf("FinishRun step2: %v", err)
	}
	children, _ = trigSvc.ListAutomationRuns(alice, &corev1.ListAutomationRunsRequest{ParentRunId: parent.GetId()})
	if len(children.GetRuns()) != 3 || children.GetRuns()[2].GetStepId() != "cleanup" {
		t.Fatalf("after step 2 failure, children = %+v, want [build, deploy, cleanup]", children.GetRuns())
	}
	step4 := children.GetRuns()[2]
	s4Start, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{RunId: step4.GetId(), DispatcherKey: dispatchSecret})
	if err != nil {
		t.Fatalf("StartRun step4: %v", err)
	}
	if _, err := runSvc.FinishRun(context.Background(), &corev1.FinishAutomationRunRequest{
		RunToken: s4Start.GetRunToken(), Outcome: "succeeded",
	}); err != nil {
		t.Fatalf("FinishRun step4: %v", err)
	}

	// Parent pipeline should now be failed (because step 2 failed even though cleanup succeeded).
	gotParent, err := trigSvc.GetAutomationRun(alice, &corev1.GetAutomationRunRequest{RunId: parent.GetId()})
	if err != nil {
		t.Fatalf("GetAutomationRun parent: %v", err)
	}
	if gotParent.GetStatus() != storage.AutomationRunFailed {
		t.Fatalf("parent status = %q, want failed", gotParent.GetStatus())
	}

	// Top-level ListAutomationRuns(pipeorg) should return only the parent run, not child steps.
	topRuns, err := trigSvc.ListAutomationRuns(alice, &corev1.ListAutomationRunsRequest{Account: "pipeorg"})
	if err != nil || len(topRuns.GetRuns()) != 1 || topRuns.GetRuns()[0].GetId() != parent.GetId() {
		t.Fatalf("top-level runs = %+v, %v", topRuns, err)
	}

	// Cascading cancel: start a second pipeline run, cancel parent -> active child step is canceled too.
	parent2, err := trigSvc.RunAutomation(alice, &corev1.RunAutomationRequest{Account: "pipeorg", AutomationId: "svc/release"})
	if err != nil {
		t.Fatal(err)
	}
	engine.Drain(context.Background())
	if _, err := trigSvc.CancelAutomationRun(alice, &corev1.CancelAutomationRunRequest{RunId: parent2.GetId()}); err != nil {
		t.Fatalf("CancelAutomationRun parent2: %v", err)
	}
	children2, _ := trigSvc.ListAutomationRuns(alice, &corev1.ListAutomationRunsRequest{ParentRunId: parent2.GetId()})
	if len(children2.GetRuns()) != 1 || children2.GetRuns()[0].GetStatus() != storage.AutomationRunCanceled {
		t.Fatalf("cascading cancel child = %+v, want canceled", children2.GetRuns())
	}
}
