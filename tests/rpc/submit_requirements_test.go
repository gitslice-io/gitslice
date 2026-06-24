package rpc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRPCSubmitApprovalRequiredSliceBlocksUntilNonAuthorApproval(t *testing.T) {
	ts := startRPCServer(t)
	aliceToken := ts.loginViaGRPC(t, "alice")
	bobToken := ts.loginViaGRPC(t, "bob")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	aliceCtx := grpcAuthContext(aliceToken)
	bobCtx := grpcAuthContext(bobToken)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, aliceCtx, clients, slices, "approval-req", 1, nil)
	changesetID, patchsetID := createDirectPatchsetForSlice(t, aliceCtx, clients, sliceRef, "/acme/payment/approval-req/change.go", "package approvalreq\nconst V = 1\n", "approval required")

	_, err := clients.changeset.SubmitChangeset(aliceCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertSubmitBlocked(t, err, "required approvals not satisfied")
	assertChangesetBlockedReason(t, aliceCtx, clients.changeset, changesetID, "required approvals not satisfied")

	if _, err := clients.changeset.ApproveChangeset(aliceCtx, &corev1.ApproveChangesetRequest{ChangesetId: changesetID}); err != nil {
		t.Fatal(err)
	}
	_, err = clients.changeset.SubmitChangeset(aliceCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertSubmitBlocked(t, err, "required approvals not satisfied")

	if _, err := clients.changeset.ApproveChangeset(bobCtx, &corev1.ApproveChangesetRequest{ChangesetId: changesetID}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(aliceCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubmittedChangeset(t, aliceCtx, clients.changeset, changesetID)
}

func TestRPCSubmitRequiredCheckBlocksUntilPassing(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "check-req", 0, []string{"unit"})
	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/check-req/change.go", "package checkreq\nconst V = 1\n", "check required")

	_, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertSubmitBlocked(t, err, `required check "unit" has no result`)
	assertChangesetBlockedReason(t, ctx, clients.changeset, changesetID, `required check "unit" has no result`)

	if _, err := clients.changeset.ReportCheckResult(ctx, &corev1.ReportCheckResultRequest{ChangesetId: changesetID, CheckName: "unit", Status: "pass"}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, changesetID)
}

func TestRPCSubmitFailingRequiredCheckBlocks(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "check-fail", 0, []string{"unit"})
	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/check-fail/change.go", "package checkfail\nconst V = 1\n", "check failing")
	if _, err := clients.changeset.ReportCheckResult(ctx, &corev1.ReportCheckResultRequest{ChangesetId: changesetID, CheckName: "unit", Status: "fail"}); err != nil {
		t.Fatal(err)
	}

	_, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertSubmitBlocked(t, err, `required check "unit" is failing`)
	assertChangesetBlockedReason(t, ctx, clients.changeset, changesetID, `required check "unit" is failing`)
}

func TestRPCSubmitDefinitionHashDriftBlocksRefresh(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "hash-drift", 0, nil)
	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/hash-drift/change.go", "package hashdrift\nconst V = 1\n", "hash drift")
	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths:     slice.Definition.IncludedPaths,
			Visibility:        slice.Definition.Visibility,
			RequiredApprovals: 1,
			RequiredChecks:    nil,
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertSubmitBlocked(t, err, "requirements changed, refresh the changeset")
	assertChangesetBlockedReason(t, ctx, clients.changeset, changesetID, "requirements changed, refresh the changeset")
}

func TestRPCSubmitVisibilityChangeDoesNotBlockRefresh(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "visibility-change", 0, nil)
	changesetID, patchsetID := createDirectPatchsetForSlice(
		t,
		ctx,
		clients,
		sliceRef,
		"/acme/payment/visibility-change/change.go",
		"package visibilitychange\nconst V = 1\n",
		"visibility change",
	)
	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths:     slice.Definition.IncludedPaths,
			Visibility:        "public",
			RequiredApprovals: slice.Definition.RequiredApprovals,
			RequiredChecks:    slice.Definition.RequiredChecks,
		},
	}); err != nil {
		t.Fatal(err)
	}

	submitted, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != "pending_publish" && submitted.Status != "submitted" {
		t.Fatalf("unexpected submit response: %#v", submitted)
	}
	cs, err := clients.changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != "pending_publish" && cs.Status != "submitted" {
		t.Fatalf("changeset status = %q, want pending_publish or submitted", cs.Status)
	}
}

func createSubmitRequirementSlice(t *testing.T, ctx context.Context, clients testCoreClients, slices corev1.SliceServiceClient, slug string, requiredApprovals int32, requiredChecks []string) *corev1.SliceRef {
	t.Helper()
	seedPath := "/acme/payment/" + slug + "/seed.txt"
	submitDirectFile(t, ctx, clients, seedPath, slug+" seed\n", slug+" seed")
	ref := &corev1.SliceRef{Account: "acme", Slice: slug}
	created, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:               ref,
		IncludedPaths:     []string{"/acme/payment/" + slug},
		Visibility:        "private",
		RequiredApprovals: requiredApprovals,
		RequiredChecks:    requiredChecks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Definition.RequiredApprovals != requiredApprovals {
		t.Fatalf("required approvals = %d, want %d", created.Definition.RequiredApprovals, requiredApprovals)
	}
	assertStringSet(t, created.Definition.RequiredChecks, requiredChecks...)
	return ref
}

func createDirectPatchsetForSlice(t *testing.T, ctx context.Context, clients testCoreClients, sliceRef *corev1.SliceRef, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if patchset.SubmitRequirements == nil {
		t.Fatalf("patchset submit requirements are nil")
	}
	return cs.Id, patchset.Id
}

func assertSubmitBlocked(t *testing.T, err error, reason string) {
	t.Helper()
	if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), reason) {
		t.Fatalf("submit err = %v, want FailedPrecondition containing %q", err, reason)
	}
}

func assertChangesetBlockedReason(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient, changesetID, reason string) {
	t.Helper()
	cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cs.SubmitBlockedReason, reason) {
		t.Fatalf("submit blocked reason = %q, want containing %q", cs.SubmitBlockedReason, reason)
	}
}
