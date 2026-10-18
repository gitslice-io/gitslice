package rpc_test

import (
	"context"
	"io"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
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

// A check run names the daemon that ran it by the name the daemon is shown by,
// and follows a rename.
func TestRPCCheckRunShowsItsDaemonByName(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	checks := corev1.NewCheckServiceClient(conn)
	agents := corev1.NewAgentServiceClient(conn)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := agents.Connect(streamCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{Name: "ci-runner", Runtime: "codex", Version: "0.1"}}}); err != nil {
		t.Fatal(err)
	}
	reg, err := stream.Recv()
	if err != nil || reg.GetRegistered().GetDaemonId() == "" {
		t.Fatalf("register: %v, %v", reg, err)
	}
	daemonID := reg.GetRegistered().GetDaemonId()

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "check-run-daemon-name", 0, []string{"unit"})
	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/check-run-daemon-name/change.go", "package daemonname\nconst V = 1\n", "check run daemon name")
	store := openRPCCheckStore(t, ts)
	run, err := store.CreateCheckRun(context.Background(), storage.CheckRunInput{
		ChangesetID: changesetID,
		PatchsetID:  patchsetID,
		CheckName:   "unit",
		DaemonID:    daemonID,
		Provenance:  "ci",
		Status:      "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.DaemonName != "ci-runner" {
		t.Fatalf("a new run's daemon name = %q, want the name the daemon registered with", run.DaemonName)
	}
	shown := func() string {
		t.Helper()
		list, err := checks.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: changesetID})
		if err != nil || len(list.Runs) != 1 {
			t.Fatalf("ListCheckRuns = %v, %v", list, err)
		}
		got, err := checks.GetCheckRun(ctx, &corev1.GetCheckRunRequest{RunId: run.Id})
		if err != nil || got.DaemonId != daemonID || got.DaemonName != list.Runs[0].DaemonName {
			t.Fatalf("GetCheckRun = %+v, %v; ListCheckRuns shows %q", got, err, list.Runs[0].DaemonName)
		}
		return got.DaemonName
	}
	if got := shown(); got != "ci-runner" {
		t.Fatalf("daemon shown on the run = %q", got)
	}
	if _, err := agents.RenameDaemon(ctx, &corev1.RenameDaemonRequest{DaemonId: daemonID, DisplayName: "Main CI"}); err != nil {
		t.Fatal(err)
	}
	if got := shown(); got != "Main CI" {
		t.Fatalf("daemon shown on the run after a rename = %q", got)
	}
}
