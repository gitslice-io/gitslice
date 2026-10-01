package rpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestRPCCIDaemonRunsUnbundledInSliceChecks checks that a slice's CI daemon
// runs in-slice checks for patchsets created without bundled results (gs
// create, git push), and does not re-run checks the author already bundled.
func TestRPCCIDaemonRunsUnbundledInSliceChecks(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	agent := corev1.NewAgentServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "ci-inslice", 0, nil)
	submitSliceChecksFile(t, ctx, clients, sliceRef, "/acme/payment/ci-inslice", `
version: 1
checks:
  unit:
    run: "echo unit"
`)

	daemonCtx, cancelDaemon := context.WithTimeout(ctx, 30*time.Second)
	defer cancelDaemon()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:              "ci-inslice-daemon",
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
	if _, err := slices.SetSliceCIDaemon(ctx, &corev1.SetSliceCIDaemonRequest{Slice: sliceRef, DaemonId: reg.GetRegistered().GetDaemonId()}); err != nil {
		t.Fatalf("SetSliceCIDaemon: %v", err)
	}

	// No bundled results: the CI daemon runs the in-slice check.
	firstCS, firstPS := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/ci-inslice/one.go", "package inslice\nconst One = 1\n", "unbundled change")
	first := recvRunChecks(t, stream, firstCS, firstPS)
	if len(first.Checks) != 1 || !strings.HasSuffix(first.Checks[0].Name, "unit") || first.Checks[0].Command != "echo unit" {
		t.Fatalf("RunChecks for an unbundled patchset = %#v, want the in-slice unit check", first.Checks)
	}
	checkName := first.Checks[0].Name

	// Bundled results: nothing is dispatched for that patchset. Messages arrive
	// in order, so the next RunChecks must belong to the later unbundled one.
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package inslice\nconst Two = 2\n"), Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	bundledCS, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "bundled change",
	})
	if err != nil {
		t.Fatal(err)
	}
	bundledPS, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  bundledCS.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{
			{Op: "add", Path: "/acme/payment/ci-inslice/two.go", BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644},
		},
		BundledCheckRuns: []*corev1.BundledCheckRun{{Name: checkName, Status: "passed", Summary: "ran locally"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thirdCS, thirdPS := createDirectPatchsetForSlice(t, ctx, clients, sliceRef, "/acme/payment/ci-inslice/three.go", "package inslice\nconst Three = 3\n", "another unbundled change")
	next := recvAnyRunChecks(t, stream)
	if next.ChangesetId == bundledCS.Id && next.PatchsetId == bundledPS.Id {
		t.Fatalf("the CI daemon re-ran a check the author bundled: %#v", next.Checks)
	}
	if next.ChangesetId != thirdCS || next.PatchsetId != thirdPS {
		t.Fatalf("next RunChecks = %s/%s, want %s/%s", next.ChangesetId, next.PatchsetId, thirdCS, thirdPS)
	}
}

func submitSliceChecksFile(t *testing.T, ctx context.Context, clients testCoreClients, sliceRef *corev1.SliceRef, dir, content string) {
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
		Title:          "slice checks",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{
			{Op: "mkdir", Path: dir + "/.gitslice"},
			{Op: "add", Path: dir + "/.gitslice/checks.yaml", BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644},
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

func recvAnyRunChecks(t *testing.T, stream corev1.AgentService_ConnectClient) *corev1.RunChecks {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv RunChecks: %v", err)
		}
		if run := msg.GetRunChecks(); run != nil {
			return run
		}
	}
	t.Fatal("timed out waiting for RunChecks")
	return nil
}
