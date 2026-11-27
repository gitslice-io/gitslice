package service

import (
	"context"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAutomationRunDiffMainReadAndResolvedChangeset(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bob := asUser(t, h, "user_bobby", "bobby")

	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "specorg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "specorg", Username: "bobby", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(bob, &corev1.RespondToInvitationRequest{Account: "specorg", Accept: true}); err != nil {
		t.Fatal(err)
	}

	homeRef := &corev1.SliceRef{Account: "specorg", Slice: "home"}
	commitSliceFile(t, alice, h, mem, homeRef, "/specorg/app/design/42_spec.md", "---\ngitslice_spec: 1\nstatus: active\nscope: [\"service/**\"]\n---\n")
	commitSliceFile(t, alice, h, mem, homeRef, "/specorg/app/service/quota.go", "package service\n\nfunc Quota() int { return 1 }\n")
	commitSliceFile(t, alice, h, mem, homeRef, "/specorg/app/.gitslice/spec-gate.js", "export default { async onEvent() {} };\n")
	commitSliceFile(t, alice, h, mem, homeRef, "/specorg/app/.gitslice/automations.yaml", `version: 1
automations:
  spec-gate:
    on: [changeset.created, changeset.updated, changeset.approved, manual]
    checks: [spec-gate]
    action:
      worker: spec-gate.js
`)

	// Create a draft changeset by bob referencing the spec.
	head, err := mem.Repository.GetRef(context.Background(), "refs/global/main")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := h.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: homeRef,
		BaseCommitId:   head.GetCommitId(),
		Title:          "service: implement quota",
		Description:    "Spec: design/42_spec.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := h.Blob.UploadBlob(bob, &corev1.UploadBlobRequest{
		Data:  []byte("package service\n\nfunc Quota() int { return 42 }\n"),
		Slice: homeRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.GetId(),
		BaseCommitId: head.GetCommitId(),
		FileEdits: []*corev1.FileEdit{
			{Path: "/specorg/app/service/quota.go", Op: "upsert", BlobId: blob.GetBlobId(), ContentHash: blob.GetContentHash(), Mode: 0o100644},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Alice approves bob's changeset so GetChangeset should expose her resolved username.
	if _, err := h.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: cs.GetId()}); err != nil {
		t.Fatal(err)
	}

	// Now alice closes the spec on main AFTER bob branched.
	commitSliceFile(t, alice, h, mem, homeRef, "/specorg/app/design/42_spec.md", "---\ngitslice_spec: 1\nstatus: closed\nscope: [\"service/**\"]\n---\n")

	dispatchSecret := "test-dispatch-secret"
	engine, trigSvc, runSvc := newAutomationTestServices(mem, dispatchSecret)
	runSvc.Blobs = mem.Blobs
	runSvc.ChangesetService = h.Changeset
	h.setAutomationEngine(engine)

	run, err := trigSvc.RunAutomation(alice, &corev1.RunAutomationRequest{
		Account:      "specorg",
		AutomationId: "app/spec-gate",
		ChangesetId:  cs.GetId(),
	})
	if err != nil {
		t.Fatalf("RunAutomation: %v", err)
	}
	started, err := runSvc.StartRun(context.Background(), &corev1.StartAutomationRunRequest{
		RunId:         run.GetId(),
		DispatcherKey: dispatchSecret,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	tok := started.GetRunToken()

	// 1. ReadFile at "base" sees the old active spec; at "main" sees the newly closed spec.
	baseSpec, err := runSvc.ReadFile(context.Background(), &corev1.AutomationReadFileRequest{
		RunToken: tok, Path: "design/42_spec.md", At: "base",
	})
	if err != nil || !baseSpec.GetFound() || !strings.Contains(string(baseSpec.GetContent()), "status: active") {
		t.Fatalf("ReadFile(base) = (%v, %q, %v), want active spec", baseSpec.GetFound(), string(baseSpec.GetContent()), err)
	}
	mainSpec, err := runSvc.ReadFile(context.Background(), &corev1.AutomationReadFileRequest{
		RunToken: tok, Path: "design/42_spec.md", At: "main",
	})
	if err != nil || !mainSpec.GetFound() || !strings.Contains(string(mainSpec.GetContent()), "status: closed") {
		t.Fatalf("ReadFile(main) = (%v, %q, %v), want closed spec", mainSpec.GetFound(), string(mainSpec.GetContent()), err)
	}
	patchFile, err := runSvc.ReadFile(context.Background(), &corev1.AutomationReadFileRequest{
		RunToken: tok, Path: "service/quota.go", At: "patchset",
	})
	if err != nil || !patchFile.GetFound() || !strings.Contains(string(patchFile.GetContent()), "return 42") {
		t.Fatalf("ReadFile(patchset) = (%v, %q, %v), want updated file", patchFile.GetFound(), string(patchFile.GetContent()), err)
	}
	if _, err := runSvc.ReadFile(context.Background(), &corev1.AutomationReadFileRequest{
		RunToken: tok, Path: "design/42_spec.md", At: "bogus",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("ReadFile(bogus) err = %v, want InvalidArgument", err)
	}

	// 2. DiffChangeset returns the unified diff scoped to the account and respects path filters.
	diffResp, err := runSvc.DiffChangeset(context.Background(), &corev1.AutomationDiffChangesetRequest{
		RunToken: tok,
		Paths:    []string{"service/quota.go"},
	})
	if err != nil {
		t.Fatalf("DiffChangeset: %v", err)
	}
	if !strings.Contains(diffResp.GetDiff(), "+func Quota() int { return 42 }") {
		t.Fatalf("DiffChangeset diff missing expected hunk:\n%s", diffResp.GetDiff())
	}
	if _, err := runSvc.DiffChangeset(context.Background(), &corev1.AutomationDiffChangesetRequest{
		RunToken: tok,
		Paths:    []string{"/otheracct/secret.go"},
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-account DiffChangeset err = %v, want PermissionDenied", err)
	}

	// 3. GetChangeset resolves author and approver usernames so workers can verify human spec owners.
	gotCS, err := runSvc.GetChangeset(context.Background(), &corev1.AutomationRunTokenRequest{RunToken: tok})
	if err != nil {
		t.Fatalf("GetChangeset: %v", err)
	}
	if gotCS.GetAuthor() != "bobby" {
		t.Fatalf("GetChangeset author = %q, want bobby", gotCS.GetAuthor())
	}
	if len(gotCS.GetApprovals()) != 1 || gotCS.GetApprovals()[0].GetUsername() != "alice" {
		t.Fatalf("GetChangeset approvals = %+v, want alice", gotCS.GetApprovals())
	}

	// 4. Automation-owned checks cannot be manually spoofed as passing, even by a non-author reviewer.
	if _, err := runSvc.CreateCheck(context.Background(), &corev1.CreateAutomationCheckRequest{
		RunToken: tok,
		Name:     "spec-gate",
		Status:   "failed",
		Summary:  "spec closed on main",
	}); err != nil {
		t.Fatalf("CreateCheck: %v", err)
	}
	if _, err := h.Changeset.ReportCheckResult(alice, &corev1.ReportCheckResultRequest{
		ChangesetId: cs.GetId(),
		CheckName:   "app/spec-gate",
		Status:      "pass",
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("manual ReportCheckResult on automation-owned check = %v, want PermissionDenied", err)
	}
}
