package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func commitFileViaAlice(t *testing.T, ctx context.Context, handlers *Handlers, mem *memory.Stores, sliceRef *corev1.SliceRef, filePath, content string) string {
	t.Helper()
	head, err := mem.Repository.GetRef(ctx, "refs/global/main")
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	cs, _ := createStandaloneChangesetWithPatchsetForSlice(t, ctx, handlers, sliceRef, head.GetCommitId(), "commit "+filePath, filePath, content)
	sub, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId: cs.GetId(),
	})
	if err != nil {
		t.Fatalf("SubmitChangeset(%s): %v", filePath, err)
	}
	if sub.GetNewRefCommitId() != "" {
		return sub.GetNewRefCommitId()
	}
	if _, err := mem.Changesets.PublishPending(context.Background(), 10); err != nil {
		t.Fatalf("PublishPending(%s): %v", filePath, err)
	}
	head, err = mem.Repository.GetRef(ctx, "refs/global/main")
	if err != nil {
		t.Fatalf("GetRef after publish: %v", err)
	}
	return head.GetCommitId()
}

func TestRoleValidationContributorAndRejectsMaintainer(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	alice := subjectCtx("user_alice")

	// Slice member: contributor accepted, maintainer rejected.
	m, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
		Role:     "contributor",
	})
	if err != nil {
		t.Fatalf("SetSliceMember(contributor): %v", err)
	}
	if m.GetRole() != "contributor" {
		t.Fatalf("SetSliceMember role = %q, want contributor", m.GetRole())
	}

	_, err = handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
		Role:     "maintainer",
	})
	wantCode(t, err, codes.InvalidArgument, "SetSliceMember maintainer")

	// Account member & invitation: create organization account so members can be managed.
	handlers.Auth.OperatorSubjects = []string{"user_alice"}
	if _, err := handlers.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{
		Slug:           "orgone",
		OwnerUsernames: []string{"alice"},
	}); err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	mem.AddAccountRole("user_bob", "orgone", "reader")
	_, err = handlers.Auth.SetAccountMember(alice, &corev1.SetAccountMemberRequest{
		Account:  "orgone",
		Username: "bob",
		Role:     "contributor",
	})
	if err != nil {
		t.Fatalf("SetAccountMember(contributor): %v", err)
	}

	_, err = handlers.Auth.SetAccountMember(alice, &corev1.SetAccountMemberRequest{
		Account:  "orgone",
		Username: "bob",
		Role:     "maintainer",
	})
	wantCode(t, err, codes.InvalidArgument, "SetAccountMember maintainer")

	// Account invitation: contributor accepted, maintainer rejected.
	inv, err := handlers.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{
		Account:  "orgone",
		Username: "carol",
		Role:     "contributor",
	})
	if err != nil {
		t.Fatalf("InviteAccountMember(contributor): %v", err)
	}
	if inv.GetInvitation().GetRole() != "contributor" {
		t.Fatalf("InviteAccountMember role = %q, want contributor", inv.GetInvitation().GetRole())
	}

	_, err = handlers.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{
		Account:  "orgone",
		Username: "carol",
		Role:     "maintainer",
	})
	wantCode(t, err, codes.InvalidArgument, "InviteAccountMember maintainer")
	_ = mem
}

func TestContributorCapabilitiesAndBoundaries(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	mem.PutSlice(paymentRef, []string{"/acme/payment"}, "private")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "secret"}, []string{"/acme/secret"}, "private")
	mem.AddPersonalAccount("user_bob", "bob")
	mem.AddPersonalAccount("user_carol", "carol")
	mem.AddPersonalAccount("user_dana", "dana")
	mem.AddPersonalAccount("user_erin", "erin")

	_, trigSvc, _ := newTriggerTestServices(mem, "test-dispatch-secret")
	handlers.Trigger = trigSvc

	alice := subjectCtx("user_alice")
	bob := subjectCtx("user_bob")     // contributor
	carol := subjectCtx("user_carol") // reader
	dana := subjectCtx("user_dana")   // writer
	erin := subjectCtx("user_erin")   // slice admin

	homeRef := &corev1.SliceRef{Account: "acme", Slice: "home"}
	hasHome := false
	if _, err := mem.Slices.Resolve(context.Background(), homeRef); err == nil {
		hasHome = true
	}
	for _, tc := range []struct {
		username string
		role     string
	}{
		{"bob", "contributor"},
		{"carol", "reader"},
		{"dana", "writer"},
		{"erin", "admin"},
	} {
		if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
			Slice:    paymentRef,
			Username: tc.username,
			Role:     tc.role,
		}); err != nil {
			t.Fatalf("SetSliceMember(%s=%s): %v", tc.username, tc.role, err)
		}
		if hasHome {
			if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
				Slice:    homeRef,
				Username: tc.username,
				Role:     tc.role,
			}); err != nil {
				t.Fatalf("SetSliceMember(home, %s=%s): %v", tc.username, tc.role, err)
			}
		}
	}

	// Commit base files, automations.yaml, and a trigger run for automation capability checks.
	triggersYAML := `version: 1
automations:
  notify:
    on: [manual]
    action:
      http:
        url: https://example.com/hook
`
	baseCommit := commitFileViaAlice(t, alice, handlers, mem, paymentRef, "/acme/payment/base.txt", "hello\n")
	baseCommit = commitFileViaAlice(t, alice, handlers, mem, paymentRef, "/acme/payment/.gitslice/automations.yaml", triggersYAML)
	if _, err := mem.Triggers.CreateTriggerRuns(context.Background(), []storage.TriggerRun{{
		ID:        "tr_authz_1",
		Account:   "acme",
		TriggerID: triggers.ID("/acme", "/acme/payment", "notify"),
		Folder:    "/acme/payment",
		Action:    triggers.ActionHTTP,
		EventID:   "evt_authz_1",
		EventKind: triggers.EventManual,
		Status:    "failed",
	}}); err != nil {
		t.Fatalf("CreateTriggerRuns: %v", err)
	}

	// Alice creates a changeset, stack, and check run to verify cross-user & check boundaries.
	aliceCS, alicePatch := createStandaloneChangesetWithPatchsetForSlice(t, alice, handlers, paymentRef, baseCommit, "alice cs", "/acme/payment/alice.txt", "alice v1\n")
	aliceStackResp, err := handlers.Stack.CreateStack(alice, &corev1.CreateStackRequest{
		AuthoringSlice: paymentRef,
		BaseCommitId:   baseCommit,
		Title:          "alice stack",
	})
	if err != nil {
		t.Fatalf("alice CreateStack: %v", err)
	}
	aliceCheckRun, err := mem.Checks.CreateCheckRun(context.Background(), storage.CheckRunInput{
		ChangesetID: aliceCS.GetId(),
		PatchsetID:  alicePatch.GetId(),
		CheckName:   "unit",
		DaemonID:    "daemon_1",
		Status:      "failed",
	})
	if err != nil {
		t.Fatalf("CreateCheckRun: %v", err)
	}

	// 1. Reader is denied ActionPropose RPCs.
	readerBlobData := []byte("reader-data")
	_, err = handlers.Blob.UploadBlob(carol, &corev1.UploadBlobRequest{Data: readerBlobData, Slice: paymentRef})
	wantCode(t, err, codes.PermissionDenied, "reader UploadBlob")

	_, err = handlers.Blob.UploadBlobs(carol, &corev1.UploadBlobsRequest{
		Slice: paymentRef,
		Blobs: []*corev1.UploadBlobsEntry{{Data: readerBlobData}},
	})
	wantCode(t, err, codes.PermissionDenied, "reader UploadBlobs")

	err = handlers.Blob.UploadBlobStream(&uploadBlobStreamServerForTest{
		ctx: carol,
		chunks: []*corev1.UploadBlobChunk{{
			Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{
				Slice:       paymentRef,
				ContentHash: objectid.RawContentHash(readerBlobData),
				Size:        int64Ptr(int64(len(readerBlobData))),
			}},
		}},
	})
	wantCode(t, err, codes.PermissionDenied, "reader UploadBlobStream")

	_, err = handlers.Workspace.ValidateWorkspaceDiff(carol, &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    &corev1.WorkspaceRef{Id: "acme/payment"},
		BaseCommitId: baseCommit,
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: "/acme/payment/r.txt"}},
	})
	wantCode(t, err, codes.PermissionDenied, "reader ValidateWorkspaceDiff")

	_, err = handlers.Changeset.CreateChangeset(carol, &corev1.CreateChangesetRequest{
		AuthoringSlice: paymentRef,
		BaseCommitId:   baseCommit,
		Title:          "reader cs",
	})
	wantCode(t, err, codes.PermissionDenied, "reader CreateChangeset")

	_, err = handlers.Stack.CreateStack(carol, &corev1.CreateStackRequest{
		AuthoringSlice: paymentRef,
		BaseCommitId:   baseCommit,
		Title:          "reader stack",
	})
	wantCode(t, err, codes.PermissionDenied, "reader CreateStack")

	// 2. Contributor is ALLOWED ActionPropose & own-changeset/own-stack mutations.
	bobData := []byte("bob blob payload")
	blobResp, err := handlers.Blob.UploadBlob(bob, &corev1.UploadBlobRequest{
		Data:  bobData,
		Slice: paymentRef,
	})
	if err != nil {
		t.Fatalf("contributor UploadBlob: %v", err)
	}
	if _, err := handlers.Blob.UploadBlobs(bob, &corev1.UploadBlobsRequest{
		Slice: paymentRef,
		Blobs: []*corev1.UploadBlobsEntry{{Data: []byte("bob batch blob")}},
	}); err != nil {
		t.Fatalf("contributor UploadBlobs: %v", err)
	}
	streamData := []byte("bob streamed payload")
	stream := &uploadBlobStreamServerForTest{
		ctx: bob,
		chunks: []*corev1.UploadBlobChunk{
			{Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{
				Slice:       paymentRef,
				ContentHash: objectid.RawContentHash(streamData),
				Size:        int64Ptr(int64(len(streamData))),
			}}},
			{Payload: &corev1.UploadBlobChunk_Data{Data: streamData}},
		},
	}
	if err := handlers.Blob.UploadBlobStream(stream); err != nil {
		t.Fatalf("contributor UploadBlobStream: %v", err)
	}
	if _, err := handlers.Workspace.ValidateWorkspaceDiff(bob, &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    &corev1.WorkspaceRef{Id: "acme/payment"},
		BaseCommitId: baseCommit,
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: "/acme/payment/bob.txt", BlobId: blobResp.GetBlobId()}},
	}); err != nil {
		t.Fatalf("contributor ValidateWorkspaceDiff: %v", err)
	}

	bobCS, err := handlers.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: paymentRef,
		BaseCommitId:   baseCommit,
		Title:          "bob cs",
	})
	if err != nil {
		t.Fatalf("contributor CreateChangeset: %v", err)
	}
	bobPatchResp, err := handlers.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  bobCS.GetId(),
		BaseCommitId: baseCommit,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   "/acme/payment/bob.txt",
			BlobId: blobResp.GetBlobId(),
			Mode:   0o644,
		}},
	})
	if err != nil {
		t.Fatalf("contributor UpdateChangeset(own): %v", err)
	}
	newTitle := "bob cs updated"
	if _, err := handlers.Changeset.UpdateChangesetInfo(bob, &corev1.UpdateChangesetInfoRequest{
		ChangesetId: bobCS.GetId(),
		Title:       &newTitle,
	}); err != nil {
		t.Fatalf("contributor UpdateChangesetInfo(own): %v", err)
	}

	// Contributor can create and manage own stack entries.
	bobStack, err := handlers.Stack.CreateStack(bob, &corev1.CreateStackRequest{
		AuthoringSlice: paymentRef,
		BaseCommitId:   baseCommit,
		Title:          "bob stack",
	})
	if err != nil {
		t.Fatalf("contributor CreateStack: %v", err)
	}
	bobEntry1, err := handlers.Stack.AddStackEntry(bob, &corev1.AddStackEntryRequest{
		StackId: bobStack.GetId(),
		Title:   "entry 1",
	})
	if err != nil {
		t.Fatalf("contributor AddStackEntry(own): %v", err)
	}
	bobEntry1Patch, err := handlers.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  bobEntry1.GetId(),
		BaseCommitId: baseCommit,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   "/acme/payment/stack1.txt",
			BlobId: blobResp.GetBlobId(),
			Mode:   0o644,
		}},
	})
	if err != nil {
		t.Fatalf("contributor UpdateChangeset(stack entry 1): %v", err)
	}
	bobEntry2, err := handlers.Stack.AddStackEntry(bob, &corev1.AddStackEntryRequest{
		StackId:           bobStack.GetId(),
		Title:             "entry 2",
		ParentChangesetId: bobEntry1.GetId(),
		ParentPatchsetId:  bobEntry1Patch.GetId(),
	})
	if err != nil {
		t.Fatalf("contributor AddStackEntry(own child): %v", err)
	}
	if _, err := handlers.Stack.DetachStackEntry(bob, &corev1.DetachStackEntryRequest{
		StackId:     bobStack.GetId(),
		ChangesetId: bobEntry2.GetId(),
	}); err != nil {
		t.Fatalf("contributor DetachStackEntry(own): %v", err)
	}

	// Contributor can abandon own spare changeset.
	spareCS, err := handlers.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: paymentRef,
		BaseCommitId:   baseCommit,
		Title:          "spare",
	})
	if err != nil {
		t.Fatalf("CreateChangeset(spare): %v", err)
	}
	if _, err := handlers.Changeset.AbandonChangeset(bob, &corev1.AbandonChangesetRequest{
		ChangesetId: spareCS.GetId(),
	}); err != nil {
		t.Fatalf("contributor AbandonChangeset(own): %v", err)
	}

	// 3. Contributor is DENIED writer+, review, tag, secret, trigger, manage-members, and cross-user writes.
	_, err = handlers.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  aliceCS.GetId(),
		BaseCommitId: baseCommit,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   "/acme/payment/hijack.txt",
			BlobId: blobResp.GetBlobId(),
			Mode:   0o644,
		}},
	})
	wantCode(t, err, codes.PermissionDenied, "contributor UpdateChangeset(other)")

	_, err = handlers.Stack.AddStackEntry(bob, &corev1.AddStackEntryRequest{
		StackId: aliceStackResp.GetId(),
		Title:   "hijack",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor AddStackEntry(other)")

	_, err = handlers.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{
		ChangesetId:               bobCS.GetId(),
		ExpectedCurrentPatchsetId: bobPatchResp.GetId(),
	})
	wantCode(t, err, codes.PermissionDenied, "contributor SubmitChangeset")

	_, err = handlers.Stack.SubmitStack(bob, &corev1.SubmitStackRequest{
		StackId: bobStack.GetId(),
	})
	wantCode(t, err, codes.PermissionDenied, "contributor SubmitStack")

	_, err = handlers.Changeset.ApproveChangeset(bob, &corev1.ApproveChangesetRequest{
		ChangesetId: aliceCS.GetId(),
	})
	wantCode(t, err, codes.PermissionDenied, "contributor ApproveChangeset")

	_, err = handlers.Check.RerunCheck(bob, &corev1.RerunCheckRequest{
		RunId: aliceCheckRun.GetId(),
	})
	wantCode(t, err, codes.PermissionDenied, "contributor RerunCheck")

	_, err = handlers.Changeset.ReportCheckResult(bob, &corev1.ReportCheckResultRequest{
		ChangesetId: bobCS.GetId(),
		CheckName:   "unit",
		Status:      "pass",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor ReportCheckResult")

	_, err = handlers.Slice.CreateTag(bob, &corev1.CreateTagRequest{
		Slice:    paymentRef,
		Name:     "v1.0.0",
		CommitId: baseCommit,
	})
	wantCode(t, err, codes.PermissionDenied, "contributor CreateTag")

	_, err = handlers.Slice.SetSliceMember(bob, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "carol",
		Role:     "writer",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor SetSliceMember")

	_, err = handlers.Slice.RemoveSliceMember(bob, &corev1.RemoveSliceMemberRequest{
		Slice:    paymentRef,
		Username: "carol",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor RemoveSliceMember(other)")

	_, err = handlers.Slice.ListSliceSecrets(bob, &corev1.ListSliceSecretsRequest{
		Slice: paymentRef,
	})
	wantCode(t, err, codes.PermissionDenied, "contributor ListSliceSecrets")

	triggerID := triggers.ID("/acme", "/acme/payment", "notify")
	_, err = handlers.Trigger.RunTrigger(bob, &corev1.RunTriggerRequest{
		Account:   "acme",
		TriggerId: triggerID,
	})
	wantCode(t, err, codes.PermissionDenied, "contributor RunTrigger")

	_, err = handlers.Trigger.RerunTriggerRun(bob, &corev1.RerunTriggerRunRequest{
		RunId: "tr_authz_1",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor RerunTriggerRun")

	_, err = handlers.Trigger.CancelTriggerRun(bob, &corev1.CancelTriggerRunRequest{
		RunId: "tr_authz_1",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor CancelTriggerRun")

	_, err = handlers.Changeset.CopyPath(bob, &corev1.CopyPathRequest{
		Slice:           paymentRef,
		SourcePath:      "/acme/payment/base.txt",
		DestinationPath: "/acme/payment/base-copy.txt",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor CopyPath")

	_, err = handlers.Changeset.MovePath(bob, &corev1.MovePathRequest{
		Slice:           paymentRef,
		SourcePath:      "/acme/payment/base.txt",
		DestinationPath: "/acme/payment/base-moved.txt",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor MovePath")

	_, err = handlers.Repository.ImportGitRepository(bob, &corev1.ImportGitRepositoryRequest{
		AuthoringSlice: paymentRef,
		MountPath:      "/acme/payment/import",
		Source:         "https://example.com/repo.git",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor ImportGitRepository")

	// 4. Writer (dana) CAN review, report checks, tag, use secrets/triggers, and submit; CANNOT manage members.
	if _, err := handlers.Changeset.ApproveChangeset(dana, &corev1.ApproveChangesetRequest{
		ChangesetId: bobCS.GetId(),
	}); err != nil {
		t.Fatalf("writer ApproveChangeset: %v", err)
	}
	if _, err := handlers.Changeset.ReportCheckResult(dana, &corev1.ReportCheckResultRequest{
		ChangesetId: bobCS.GetId(),
		CheckName:   "unit",
		Status:      "pass",
	}); err != nil {
		t.Fatalf("writer ReportCheckResult: %v", err)
	}
	if _, err := handlers.Slice.CreateTag(dana, &corev1.CreateTagRequest{
		Slice:    paymentRef,
		Name:     "v1.0.0",
		CommitId: baseCommit,
	}); err != nil {
		t.Fatalf("writer CreateTag: %v", err)
	}
	if _, err := handlers.Slice.ListSliceSecrets(dana, &corev1.ListSliceSecretsRequest{
		Slice: paymentRef,
	}); err != nil {
		t.Fatalf("writer ListSliceSecrets: %v", err)
	}
	// Triggers may check account-level or covering-slice capabilities; verify contributor vs writer at both levels.
	mem.AddAccountRole("user_bob", "acme", "contributor")
	_, err = handlers.Trigger.RunTrigger(bob, &corev1.RunTriggerRequest{
		Account:   "acme",
		TriggerId: triggerID,
	})
	wantCode(t, err, codes.PermissionDenied, "contributor account RunTrigger")
	_, err = handlers.Trigger.RerunTriggerRun(bob, &corev1.RerunTriggerRunRequest{
		RunId: "tr_authz_1",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor account RerunTriggerRun")
	_, err = handlers.Trigger.CancelTriggerRun(bob, &corev1.CancelTriggerRunRequest{
		RunId: "tr_authz_1",
	})
	wantCode(t, err, codes.PermissionDenied, "contributor account CancelTriggerRun")

	mem.AddAccountRole("user_dana", "acme", "writer")
	runResp, err := handlers.Trigger.RunTrigger(dana, &corev1.RunTriggerRequest{
		Account:   "acme",
		TriggerId: triggerID,
	})
	if err != nil {
		t.Fatalf("writer RunTrigger: %v", err)
	}
	if _, err := handlers.Trigger.CancelTriggerRun(dana, &corev1.CancelTriggerRunRequest{
		RunId: runResp.GetId(),
	}); err != nil {
		t.Fatalf("writer CancelTriggerRun: %v", err)
	}
	if _, err := handlers.Trigger.RerunTriggerRun(dana, &corev1.RerunTriggerRunRequest{
		RunId: "tr_authz_1",
	}); err != nil {
		t.Fatalf("writer RerunTriggerRun: %v", err)
	}
	// A writer who does not own the changeset cannot submit it (design/36 section 2) ...
	_, err = handlers.Changeset.SubmitChangeset(dana, &corev1.SubmitChangesetRequest{
		ChangesetId:               bobCS.GetId(),
		ExpectedCurrentPatchsetId: bobPatchResp.GetId(),
	})
	wantCode(t, err, codes.PermissionDenied, "non-owner writer SubmitChangeset")
	// ... until a slice admin makes it a collaborator.
	if _, err := handlers.Changeset.AddChangesetCollaborator(erin, &corev1.AddChangesetCollaboratorRequest{
		ChangesetId: bobCS.GetId(),
		Username:    "dana",
	}); err != nil {
		t.Fatalf("admin AddChangesetCollaborator: %v", err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(dana, &corev1.SubmitChangesetRequest{
		ChangesetId:               bobCS.GetId(),
		ExpectedCurrentPatchsetId: bobPatchResp.GetId(),
	}); err != nil {
		t.Fatalf("collaborator writer SubmitChangeset: %v", err)
	}

	_, err = handlers.Slice.SetSliceMember(dana, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "carol",
		Role:     "contributor",
	})
	wantCode(t, err, codes.PermissionDenied, "writer SetSliceMember")

	// 5. Slice admin (erin) CAN manage members.
	if _, err := handlers.Slice.SetSliceMember(erin, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "carol",
		Role:     "contributor",
	}); err != nil {
		t.Fatalf("slice admin SetSliceMember: %v", err)
	}
}

func TestContributorPatchsetRunsSecretlessCIUntilWriterApproves(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	mem.AddPersonalAccount("user_dana", "dana")

	alice := subjectCtx("user_alice")
	bob := subjectCtx("user_bob")   // contributor
	dana := subjectCtx("user_dana") // writer

	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
		Role:     "contributor",
	}); err != nil {
		t.Fatalf("SetSliceMember(bob=contributor): %v", err)
	}
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "dana",
		Role:     "writer",
	}); err != nil {
		t.Fatalf("SetSliceMember(dana=writer): %v", err)
	}

	checksYAML := `version: 1
checks:
  unit:
    run: echo ok
`
	baseCommit := commitFileViaAlice(t, alice, handlers, mem, paymentRef, "/acme/payment/.gitslice/checks.yaml", checksYAML)

	// Create account secret scoped to /acme.
	if _, err := handlers.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "acme",
		Name:    "API_TOKEN",
		Value:   "super-secret-value",
		Scope:   &corev1.SecretScope{Paths: []string{"/acme"}},
	}); err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	// Register an online CI daemon and attach it to acme/payment.
	paymentSlice, err := mem.Slices.Resolve(context.Background(), paymentRef)
	if err != nil {
		t.Fatalf("Slices.Resolve: %v", err)
	}
	daemon, err := mem.Agents.RegisterDaemon(context.Background(), storage.AgentDaemonInput{
		SubjectID: "user_alice",
		Account:   "acme",
		Name:      "ci-runner",
	})
	if err != nil {
		t.Fatalf("RegisterDaemon: %v", err)
	}
	if err := mem.Agents.ExposeDaemon(context.Background(), daemon.GetId(), paymentSlice.GetId(), "user_alice"); err != nil {
		t.Fatalf("ExposeDaemon: %v", err)
	}
	if _, err := mem.Slices.SetCIDaemons(context.Background(), paymentSlice.GetId(), []string{daemon.GetId()}); err != nil {
		t.Fatalf("SetCIDaemons: %v", err)
	}
	conn := handlers.Agent.hub.registerDaemon(daemon.GetId(), "inst-1", 4)
	defer handlers.Agent.hub.unregisterDaemon(conn)

	// Contributor (bob) creates a patchset touching normal code (not .gitslice/).
	cs, _ := createDispatchTestPatchset(t, bob, handlers, paymentRef, baseCommit, "/acme/payment/app.go", "package payment\n", "contributor patch")

	// Contributor CI runs automatically, but WITHOUT secrets.
	msg1 := waitForRunChecksMessage(t, conn)
	if len(msg1.GetChecks()) != 1 {
		t.Fatalf("first RunChecks checks len = %d, want 1", len(msg1.GetChecks()))
	}
	check1 := msg1.GetChecks()[0]
	if got := check1.GetEnv()["API_TOKEN"]; got != "" {
		t.Fatalf("contributor initial CI leaked API_TOKEN=%q, want empty", got)
	}
	for _, k := range check1.GetSecretEnv() {
		if k == "API_TOKEN" {
			t.Fatalf("contributor initial CI SecretEnv unexpectedly included API_TOKEN: %v", check1.GetSecretEnv())
		}
	}

	// Contributor cannot rerun checks directly.
	_, err = handlers.Check.RerunCheck(bob, &corev1.RerunCheckRequest{
		RunId: check1.GetRunId(),
	})
	wantCode(t, err, codes.PermissionDenied, "contributor RerunCheck before approval")

	// Mark run1 failed so rerun is permitted (RerunCheck rejects queued/running checks unless terminal).
	if _, err := mem.Checks.UpdateCheckRunStatus(context.Background(), check1.GetRunId(), "failed", 1, "needs secrets"); err != nil {
		t.Fatalf("UpdateCheckRunStatus: %v", err)
	}

	// Writer (dana != bob) approves the changeset and reruns the check -> secrets are now injected.
	if _, err := handlers.Changeset.ApproveChangeset(dana, &corev1.ApproveChangesetRequest{
		ChangesetId: cs.GetId(),
	}); err != nil {
		t.Fatalf("writer ApproveChangeset: %v", err)
	}
	if _, err := handlers.Check.RerunCheck(dana, &corev1.RerunCheckRequest{
		RunId: check1.GetRunId(),
	}); err != nil {
		t.Fatalf("writer RerunCheck: %v", err)
	}

	msg2 := waitForRunChecksMessage(t, conn)
	if len(msg2.GetChecks()) != 1 {
		t.Fatalf("rerun RunChecks checks len = %d, want 1", len(msg2.GetChecks()))
	}
	check2 := msg2.GetChecks()[0]
	if got := check2.GetEnv()["API_TOKEN"]; got != "super-secret-value" {
		t.Fatalf("approved rerun Env[API_TOKEN] = %q, want %q", got, "super-secret-value")
	}
	foundSecretEnv := false
	for _, k := range check2.GetSecretEnv() {
		if strings.EqualFold(k, "API_TOKEN") {
			foundSecretEnv = true
			break
		}
	}
	if !foundSecretEnv {
		t.Fatalf("approved rerun SecretEnv = %v, want API_TOKEN", check2.GetSecretEnv())
	}
}

func TestAuthorChangesetMutationsRequireActiveAccessAndUnarchivedSlice(t *testing.T) {
	mem, handlers := newSliceMemberHandlers(t)
	alice := subjectCtx("user_alice")
	bob := subjectCtx("user_bob")

	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
		Role:     "contributor",
	}); err != nil {
		t.Fatalf("SetSliceMember(bob=contributor): %v", err)
	}
	head, err := mem.Repository.GetRef(alice, "refs/global/main")
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	cs, err := handlers.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: paymentRef,
		BaseCommitId:   head.GetCommitId(),
		Title:          "bob initial",
	})
	if err != nil {
		t.Fatalf("CreateChangeset: %v", err)
	}

	// Downgrading author from contributor to reader revokes UpdateChangesetInfo and AbandonChangeset.
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
		Role:     "reader",
	}); err != nil {
		t.Fatalf("SetSliceMember(bob=reader): %v", err)
	}
	downgradeTitle := "should fail after downgrade"
	_, err = handlers.Changeset.UpdateChangesetInfo(bob, &corev1.UpdateChangesetInfoRequest{
		ChangesetId: cs.GetId(),
		Title:       &downgradeTitle,
	})
	wantCode(t, err, codes.PermissionDenied, "downgraded reader author UpdateChangesetInfo")
	_, err = handlers.Changeset.AbandonChangeset(bob, &corev1.AbandonChangesetRequest{
		ChangesetId: cs.GetId(),
	})
	wantCode(t, err, codes.PermissionDenied, "downgraded reader author AbandonChangeset")

	// Removing author from the slice also revokes UpdateChangesetInfo and AbandonChangeset.
	if _, err := handlers.Slice.RemoveSliceMember(alice, &corev1.RemoveSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
	}); err != nil {
		t.Fatalf("RemoveSliceMember(bob): %v", err)
	}
	unsharedTitle := "should fail after unshare"
	_, err = handlers.Changeset.UpdateChangesetInfo(bob, &corev1.UpdateChangesetInfoRequest{
		ChangesetId: cs.GetId(),
		Title:       &unsharedTitle,
	})
	wantCode(t, err, codes.PermissionDenied, "unshared author UpdateChangesetInfo")
	_, err = handlers.Changeset.AbandonChangeset(bob, &corev1.AbandonChangesetRequest{
		ChangesetId: cs.GetId(),
	})
	wantCode(t, err, codes.PermissionDenied, "unshared author AbandonChangeset")

	// Archiving the slice blocks UpdateChangesetInfo and AbandonChangeset even for the restored author.
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    paymentRef,
		Username: "bob",
		Role:     "contributor",
	}); err != nil {
		t.Fatalf("SetSliceMember(bob=contributor restore): %v", err)
	}
	if _, err := handlers.Slice.SetSliceArchived(alice, &corev1.SetSliceArchivedRequest{
		Slice:    paymentRef,
		Archived: true,
	}); err != nil {
		t.Fatalf("SetSliceArchived(true): %v", err)
	}
	archivedTitle := "should fail while archived"
	_, err = handlers.Changeset.UpdateChangesetInfo(bob, &corev1.UpdateChangesetInfoRequest{
		ChangesetId: cs.GetId(),
		Title:       &archivedTitle,
	})
	wantCode(t, err, codes.FailedPrecondition, "archived slice author UpdateChangesetInfo")
	_, err = handlers.Changeset.AbandonChangeset(bob, &corev1.AbandonChangesetRequest{
		ChangesetId: cs.GetId(),
	})
	wantCode(t, err, codes.FailedPrecondition, "archived slice author AbandonChangeset")
}
