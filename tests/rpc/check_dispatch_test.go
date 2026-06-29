package rpc_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/internal/postgres"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestRPCFullTreeRunnerDispatchSatisfiesSubmitGate(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	agent := corev1.NewAgentServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "ci-dispatch", 0, []string{"root-required"})
	submitAcmeRootChecksFile(t, ctx, clients, `
version: 1
checks:
  root-required:
    run: "echo root"
`)

	daemonCtx, cancelDaemon := context.WithTimeout(ctx, 20*time.Second)
	defer cancelDaemon()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:              "ci-dispatch-daemon",
		Runtime:           "test",
		Version:           "0.0.1",
		ContainerRuntimes: []string{"none"},
		AllowHostExec:     true,
	}}}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	reg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	daemonID := reg.GetRegistered().GetDaemonId()
	if daemonID == "" {
		t.Fatalf("registered message missing daemon id: %#v", reg)
	}
	if _, err := slices.SetSliceCIDaemon(ctx, &corev1.SetSliceCIDaemonRequest{
		Slice:    sliceRef,
		DaemonId: daemonID,
	}); err != nil {
		t.Fatalf("SetSliceCIDaemon: %v", err)
	}

	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/ci-dispatch/change.go", "package cidispatch\nconst V = 1\n", "ci dispatch")
	runChecks := recvRunChecks(t, stream, changesetID, patchsetID)
	if runChecks.ResultTreeId == "" {
		t.Fatalf("RunChecks missing result_tree_id: %#v", runChecks)
	}
	if runChecks.SliceId == "" || runChecks.Slice == nil || runChecks.Slice.Account != "acme" || runChecks.Slice.Slice != "ci-dispatch" {
		t.Fatalf("RunChecks slice context = %#v", runChecks)
	}
	if len(runChecks.Checks) != 1 {
		t.Fatalf("RunChecks checks = %d, want 1: %#v", len(runChecks.Checks), runChecks.Checks)
	}
	check := runChecks.Checks[0]
	if check.RunId == "" || check.Name != "root-required" || check.Command != "echo root" {
		t.Fatalf("RunChecks check = %#v, want root-required", check)
	}

	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_CheckUpdate{CheckUpdate: &corev1.CheckRunUpdate{
		RunId:     check.RunId,
		Status:    "passed",
		LogChunk:  "root ok\n",
		Stream:    "stdout",
		ClientSeq: 1,
		Final:     true,
		Summary:   "root ok",
	}}}); err != nil {
		t.Fatalf("send CheckRunUpdate: %v", err)
	}
	recvCheckRunAck(t, stream, check.RunId, 1)

	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}); err != nil {
		t.Fatalf("SubmitChangeset after ci pass: %v", err)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, changesetID)
}

func TestRPCSliceSecretsInjectIntoFullTreeRunnerDispatch(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	agent := corev1.NewAgentServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "ci-secret-dispatch", 0, []string{"secret-required"})
	submitAcmeRootChecksFile(t, ctx, clients, `
version: 1
checks:
  secret-required:
    run: "echo secret"
    env:
      CI_TOKEN: declared
      PLAIN_ENV: plain
`)

	daemonCtx, cancelDaemon := context.WithTimeout(ctx, 20*time.Second)
	defer cancelDaemon()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:              "ci-secret-dispatch-daemon",
		Runtime:           "test",
		Version:           "0.0.1",
		ContainerRuntimes: []string{"none"},
		AllowHostExec:     true,
	}}}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	reg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	daemonID := reg.GetRegistered().GetDaemonId()
	if daemonID == "" {
		t.Fatalf("registered message missing daemon id: %#v", reg)
	}
	if _, err := slices.SetSliceCIDaemon(ctx, &corev1.SetSliceCIDaemonRequest{
		Slice:    sliceRef,
		DaemonId: daemonID,
	}); err != nil {
		t.Fatalf("SetSliceCIDaemon: %v", err)
	}
	if _, err := slices.SetSliceSecret(ctx, &corev1.SetSliceSecretRequest{
		Slice: sliceRef,
		Name:  "CI_TOKEN",
		Value: "super-secret",
	}); err != nil {
		t.Fatalf("SetSliceSecret: %v", err)
	}
	list, err := slices.ListSliceSecrets(ctx, &corev1.ListSliceSecretsRequest{Slice: sliceRef})
	if err != nil {
		t.Fatalf("ListSliceSecrets: %v", err)
	}
	if len(list.Names) != 1 || list.Names[0] != "CI_TOKEN" {
		t.Fatalf("ListSliceSecrets names = %#v, want CI_TOKEN only", list.Names)
	}
	if strings.Contains(list.String(), "super-secret") {
		t.Fatal("ListSliceSecrets returned a secret value")
	}

	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/ci-secret-dispatch/change.go", "package cisecretdispatch\nconst V = 1\n", "ci secret dispatch")
	runChecks := recvRunChecks(t, stream, changesetID, patchsetID)
	if len(runChecks.Checks) != 1 {
		t.Fatalf("RunChecks checks = %d, want 1", len(runChecks.Checks))
	}
	check := runChecks.Checks[0]
	if check.Env["CI_TOKEN"] != "super-secret" {
		t.Fatal("RunChecks env did not contain the slice secret")
	}
	if check.Env["PLAIN_ENV"] != "plain" {
		t.Fatalf("PLAIN_ENV = %q, want plain", check.Env["PLAIN_ENV"])
	}

	if _, err := slices.DeleteSliceSecret(ctx, &corev1.DeleteSliceSecretRequest{
		Slice: sliceRef,
		Name:  "CI_TOKEN",
	}); err != nil {
		t.Fatalf("DeleteSliceSecret: %v", err)
	}
	list, err = slices.ListSliceSecrets(ctx, &corev1.ListSliceSecretsRequest{Slice: sliceRef})
	if err != nil {
		t.Fatalf("ListSliceSecrets after delete: %v", err)
	}
	if len(list.Names) != 0 {
		t.Fatalf("ListSliceSecrets after delete names = %#v, want none", list.Names)
	}
}

func TestRPCSkippedRequiredCheckDoesNotBlockSubmit(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "ci-skipped", 0, []string{"skip-required"})
	submitAcmeRootChecksFile(t, ctx, clients, `
version: 1
checks:
  skip-required:
    run: "echo docs"
    paths: ["docs/**"]
`)

	changesetID, patchsetID := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/ci-skipped/change.go", "package ciskipped\nconst V = 1\n", "ci skipped")
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}); err != nil {
		t.Fatalf("SubmitChangeset with skipped required check: %v", err)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, changesetID)
}

func submitAcmeRootChecksFile(t *testing.T, ctx context.Context, clients testCoreClients, content string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "root checks",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{
			{Op: "mkdir", Path: "/acme/.gitslice"},
			{Op: "add", Path: "/acme/.gitslice/checks.yaml", BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id)
}

func recvRunChecks(t *testing.T, stream corev1.AgentService_ConnectClient, changesetID, patchsetID string) *corev1.RunChecks {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv RunChecks: %v", err)
		}
		run := msg.GetRunChecks()
		if run == nil {
			continue
		}
		if run.ChangesetId == changesetID && run.PatchsetId == patchsetID {
			return run
		}
	}
	t.Fatalf("timed out waiting for RunChecks for %s/%s", changesetID, patchsetID)
	return nil
}

func recvCheckRunAck(t *testing.T, stream corev1.AgentService_ConnectClient, runID string, seq int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			t.Fatalf("recv CheckRunAck: EOF")
		}
		if err != nil {
			t.Fatalf("recv CheckRunAck: %v", err)
		}
		ack := msg.GetCheckAck()
		if ack == nil {
			continue
		}
		if ack.RunId == runID && ack.AckedClientSeq == seq {
			return
		}
	}
	t.Fatalf("timed out waiting for CheckRunAck for %s seq %d", runID, seq)
}
