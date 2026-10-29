package rpc_test

import (
	"context"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestRPCTriggerCheckAction: a trigger's check action runs a
// started_by: trigger check on the slice's CI daemon, linked to the trigger
// run; the run finishes when the daemon reports, and the check gates submit
// (design/34_triggers.md). An ordinary check still starts with the patchset.
func TestRPCTriggerCheckAction(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	agent := corev1.NewAgentServiceClient(conn)
	triggerClient := corev1.NewTriggerServiceClient(conn)
	checkClient := corev1.NewCheckServiceClient(conn)

	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/checks.yaml", `
version: 1
checks:
  unit:
    run: "echo unit"
    started_by: trigger
  lint:
    run: "echo lint"
    paths: ["**/*.go"]
`, "checks")
	submitDirectFile(t, ctx, clients, "/acme/payment/.gitslice/triggers.yaml", `
version: 1
triggers:
  ci:
    on: [changeset.created, changeset.updated]
    action:
      check: [unit]
`, "ci trigger")
	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "trig-ci", 0, []string{"payment/unit"})

	daemonCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "trigger-ci-daemon", Runtime: "test", Version: "0.0.1", ContainerRuntimes: []string{"none"}, AllowHostExec: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	reg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.SetSliceCIDaemon(ctx, &corev1.SetSliceCIDaemonRequest{Slice: sliceRef, DaemonId: reg.GetRegistered().GetDaemonId()}); err != nil {
		t.Fatal(err)
	}

	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/trig-ci/main.go", "package trigci\n", "trigger ci")
	specs := map[string]*corev1.CheckRunSpec{}
	deadline := time.Now().Add(20 * time.Second)
	for len(specs) < 2 && time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if rc := msg.GetRunChecks(); rc != nil && rc.ChangesetId == changesetID && rc.PatchsetId == patchsetID {
			for _, spec := range rc.Checks {
				specs[spec.Name] = spec
			}
		}
	}
	if specs["payment/unit"] == nil || specs["payment/lint"] == nil {
		t.Fatalf("daemon got %v, want payment/unit (from the trigger) and payment/lint (with the patchset)", specs)
	}
	unit, err := checkClient.GetCheckRun(ctx, &corev1.GetCheckRunRequest{RunId: specs["payment/unit"].RunId})
	if err != nil {
		t.Fatal(err)
	}
	lint, err := checkClient.GetCheckRun(ctx, &corev1.GetCheckRunRequest{RunId: specs["payment/lint"].RunId})
	if err != nil {
		t.Fatal(err)
	}
	if unit.TriggerRunId == "" || unit.Provenance != "ci" || lint.TriggerRunId != "" {
		t.Fatalf("unit = %v, lint = %v", unit, lint)
	}
	run, err := triggerClient.GetTriggerRun(ctx, &corev1.GetTriggerRunRequest{RunId: unit.TriggerRunId})
	if err != nil || run.Status != "waiting" || run.TriggerId != "payment/ci" {
		t.Fatalf("trigger run = %v, %v", run, err)
	}

	for i, spec := range []*corev1.CheckRunSpec{specs["payment/unit"], specs["payment/lint"]} {
		if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_CheckUpdate{CheckUpdate: &corev1.CheckRunUpdate{
			RunId: spec.RunId, Status: "passed", ClientSeq: int64(i + 1), Final: true, LogChunk: "ok\n", Stream: "stdout",
		}}}); err != nil {
			t.Fatal(err)
		}
		recvCheckRunAck(t, stream, spec.RunId, int64(i+1))
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if run, err = triggerClient.GetTriggerRun(ctx, &corev1.GetTriggerRunRequest{RunId: unit.TriggerRunId}); err == nil && run.Status == "succeeded" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if run.Status != "succeeded" {
		t.Fatalf("trigger run after its check passed = %v", run)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: changesetID}); err != nil {
		t.Fatalf("submit: %v", err)
	}
}
