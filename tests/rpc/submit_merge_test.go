package rpc_test

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// draftFileEdit makes a draft changeset in acme/payment that writes path,
// based on the head as it is now.
func draftFileEdit(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: testPaymentSliceRef(),
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
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: path, BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs.Id, patchset.Id
}

func readHeadFile(t *testing.T, ctx context.Context, clients testCoreClients, path string) string {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	file, err := clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: path, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	return string(file.Data)
}

func TestRPCMergeOnSubmitMergesAStaleChangeset(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	const path = "/acme/payment/merge/notes.txt"

	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "one\ntwo\nthree\nfour\n", "merge seed")
	csID, patchsetID := draftFileEdit(t, ctx, clients, path, "ONE\ntwo\nthree\nfour\n", "change the first line")
	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "one\ntwo\nthree\nFOUR\n", "upstream changes the last line")

	// Off (the default): a stale changeset is refused, as before.
	_, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: csID, ExpectedCurrentPatchsetId: patchsetID})
	assertSubmitBlocked(t, err, storage.StaleBaseReason)

	updated, err := slices.SetSliceMergeOnSubmit(ctx, &corev1.SetSliceMergeOnSubmitRequest{Slice: testPaymentSliceRef(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.MergeOnSubmit {
		t.Fatal("merge_on_submit did not turn on")
	}

	// On: the same submit, still naming the patchset it was made at, is
	// merged onto the latest base and lands with both changes.
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: csID, ExpectedCurrentPatchsetId: patchsetID}); err != nil {
		t.Fatal(err)
	}
	landed := waitForSubmittedChangeset(t, ctx, clients.changeset, csID)
	if len(landed.Patchsets) != 2 {
		t.Fatalf("changeset has %d patchsets, want the original and the merge", len(landed.Patchsets))
	}
	merged := landed.Patchsets[len(landed.Patchsets)-1]
	if merged.Kind != storage.SubmitMergeKind || landed.CurrentPatchsetId != merged.Id {
		t.Fatalf("landed patchset kind %q (current %s), want the server's merge", merged.Kind, landed.CurrentPatchsetId)
	}
	if got := readHeadFile(t, ctx, clients, path); got != "ONE\ntwo\nthree\nFOUR\n" {
		t.Fatalf("landed file = %q, want both changes", got)
	}

	// Overlapping changes are refused with the path, and no patchset is made.
	csID, patchsetID = draftFileEdit(t, ctx, clients, path, "ONE\nmine\nthree\nFOUR\n", "change the second line")
	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "ONE\ntheirs\nthree\nFOUR\n", "upstream changes the second line too")
	_, err = clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: csID, ExpectedCurrentPatchsetId: patchsetID})
	if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "gs sync") {
		t.Fatalf("overlapping submit err = %v, want FailedPrecondition naming %s and gs sync", err, path)
	}
	refused, err := clients.changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: csID})
	if err != nil {
		t.Fatal(err)
	}
	if len(refused.Patchsets) != 1 || refused.Status == "submitted" {
		t.Fatalf("a conflicting merge left %d patchsets, status %s", len(refused.Patchsets), refused.Status)
	}
	if got := readHeadFile(t, ctx, clients, path); got != "ONE\ntheirs\nthree\nFOUR\n" {
		t.Fatalf("head file = %q after a refused merge", got)
	}
}

func TestRPCMergeOnSubmitMergesAStaleStackRoot(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	const path = "/acme/payment/merge/stack.txt"

	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "a\nb\nc\n", "stack seed")
	if _, err := slices.SetSliceMergeOnSubmit(ctx, &corev1.SetSliceMergeOnSubmitRequest{Slice: testPaymentSliceRef(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := clients.stack.CreateStack(ctx, &corev1.CreateStackRequest{AuthoringSlice: testPaymentSliceRef(), TargetRef: postgres.DefaultTargetRef, BaseCommitId: ref.CommitId, Title: "merge stack"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := clients.stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{StackId: stack.Id, Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("A\nb\nc\n"), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  root.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: path, BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644}},
	}); err != nil {
		t.Fatal(err)
	}
	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "a\nb\nC\n", "upstream changes the last line")

	res, err := clients.stack.SubmitStack(ctx, &corev1.SubmitStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Results[0].Status == "blocked" {
		t.Fatalf("stack submit = %#v, want the root merged and submitted", res.Results)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, root.Id)
	if got := readHeadFile(t, ctx, clients, path); got != "A\nb\nC\n" {
		t.Fatalf("landed file = %q, want both changes", got)
	}
}

// A slice with a required check runs it again on the merge: the submit says
// it merged and that the check has no result yet, and the client, still
// naming its own patchset, lands the merge once the check passes.
func TestRPCMergeOnSubmitWaitsForTheMergesChecks(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	const path = "/acme/payment/mergecheck/notes.txt"

	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "one\ntwo\nthree\n", "seed")
	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "mergecheck", 0, []string{"unit"})
	if _, err := slices.SetSliceMergeOnSubmit(ctx, &corev1.SetSliceMergeOnSubmitRequest{Slice: sliceRef, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	csID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, path, "ONE\ntwo\nthree\n", "change the first line")
	if _, err := clients.changeset.ReportCheckResult(ctx, &corev1.ReportCheckResultRequest{ChangesetId: csID, CheckName: "unit", Status: "pass"}); err != nil {
		t.Fatal(err)
	}
	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "one\ntwo\nTHREE\n", "upstream")

	_, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: csID, ExpectedCurrentPatchsetId: patchsetID})
	if err == nil || !strings.Contains(err.Error(), "merged the changeset onto the latest base as patchset 2") || !strings.Contains(err.Error(), `required check "unit" has no result`) {
		t.Fatalf("submit err = %v, want the merge noted and the check pending", err)
	}
	if _, err := clients.changeset.ReportCheckResult(ctx, &corev1.ReportCheckResultRequest{ChangesetId: csID, CheckName: "unit", Status: "pass"}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: csID, ExpectedCurrentPatchsetId: patchsetID}); err != nil {
		t.Fatalf("resubmit naming the original patchset: %v", err)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, csID)
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	file, err := clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: path, Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	if string(file.Data) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("landed file = %q", file.Data)
	}
}
