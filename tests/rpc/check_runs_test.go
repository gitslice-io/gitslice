package rpc_test

import (
	"context"
	"io"
	"testing"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestRPCCheckRunPassingResultSatisfiesSubmitGate(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	checks := corev1.NewCheckServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "check-run-pass", 0, []string{"unit"})
	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/check-run-pass/change.go", "package checkrunpass\nconst V = 1\n", "check run pass")

	_, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertSubmitBlocked(t, err, `required check "unit" has no result`)

	store := openRPCCheckStore(t, ts)
	run, err := store.CreateCheckRun(context.Background(), storage.CheckRunInput{
		ChangesetID: changesetID,
		PatchsetID:  patchsetID,
		CheckName:   "unit",
		Provenance:  "ci",
		Status:      "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := store.AppendCheckRunLog(context.Background(), run.Id, 1, "stdout", "unit ok\n"); err != nil {
		t.Fatal(err)
	} else if !inserted {
		t.Fatalf("first check log append was not inserted")
	}
	run, err = store.UpdateCheckRunStatus(context.Background(), run.Id, "passed", 0, "unit ok")
	if err != nil {
		t.Fatal(err)
	}

	list, err := checks.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: changesetID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Runs) != 1 {
		t.Fatalf("ListCheckRuns returned %d runs, want 1: %#v", len(list.Runs), list.Runs)
	}
	if got := list.Runs[0]; got.Id != run.Id || got.Status != "passed" || got.CheckName != "unit" || got.PatchsetId != patchsetID {
		t.Fatalf("listed run = %#v, want passed unit run %#v", got, run)
	}
	got, err := checks.GetCheckRun(ctx, &corev1.GetCheckRunRequest{RunId: run.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Id != run.Id || got.ChangesetId != changesetID || got.Status != "passed" {
		t.Fatalf("GetCheckRun = %#v, want %#v", got, run)
	}
	stream, err := checks.StreamCheckRun(ctx, &corev1.StreamCheckRunRequest{RunId: run.Id})
	if err != nil {
		t.Fatal(err)
	}
	log, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if log.Seq != 1 || log.Stream != "stdout" || log.Chunk != "unit ok\n" {
		t.Fatalf("streamed log = %#v, want seq 1 stdout chunk", log)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("second stream Recv error = %v, want EOF", err)
	}

	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, changesetID)
}

func TestRPCCheckRunFailedResultKeepsSubmitBlocked(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "check-run-fail", 0, []string{"unit"})
	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/check-run-fail/change.go", "package checkrunfail\nconst V = 1\n", "check run fail")

	store := openRPCCheckStore(t, ts)
	run, err := store.CreateCheckRun(context.Background(), storage.CheckRunInput{
		ChangesetID: changesetID,
		PatchsetID:  patchsetID,
		CheckName:   "unit",
		Provenance:  "ci",
		Status:      "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateCheckRunStatus(context.Background(), run.Id, "failed", 1, "unit failed"); err != nil {
		t.Fatal(err)
	}

	_, err = clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertSubmitBlocked(t, err, `required check "unit" is failing`)
	assertChangesetBlockedReason(t, ctx, clients.changeset, changesetID, `required check "unit" is failing`)
}

func openRPCCheckStore(t *testing.T, ts *testRPCServer) storage.CheckStore {
	t.Helper()
	db, err := postgres.Open(context.Background(), databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.Checks()
}
