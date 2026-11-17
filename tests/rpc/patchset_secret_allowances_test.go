package rpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRPCAllowPatchsetSecretsEndToEnd(t *testing.T) {
	ts := startRPCServer(t)
	aliceToken := ts.loginViaGRPC(t, "alice")
	aliceCtx := grpcAuthContext(aliceToken)

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	agent := corev1.NewAgentServiceClient(conn)
	checks := corev1.NewCheckServiceClient(conn)

	sliceRef := createSubmitRequirementSlice(t, aliceCtx, clients, slices, "sec-allow-rpc", 0, []string{"sec-allow-rpc/unit"})
	submitDirectFile(t, aliceCtx, clients, "/acme/payment/sec-allow-rpc/.gitslice/checks.yaml", `
version: 1
checks:
  unit:
    run: "echo ok"
`, "rpc sec allow checks")

	// Provision contributor (slice-level), writer (slice-level), and account admin.
	contribToken, contribUser, _ := ts.provisionAccount(t, "rpccontrib", "rpc-contrib")
	writerToken, writerUser, _ := ts.provisionAccount(t, "rpcwriter", "rpc-writer")
	adminToken := ts.acmeMemberToken(t, "rpcacctadmin", "admin")

	if _, err := slices.SetSliceMember(aliceCtx, &corev1.SetSliceMemberRequest{
		Slice: sliceRef, Username: contribUser, Role: "contributor",
	}); err != nil {
		t.Fatalf("SetSliceMember(contributor): %v", err)
	}
	if _, err := slices.SetSliceMember(aliceCtx, &corev1.SetSliceMemberRequest{
		Slice: sliceRef, Username: writerUser, Role: "writer",
	}); err != nil {
		t.Fatalf("SetSliceMember(writer): %v", err)
	}

	contribCtx := grpcAuthContext(contribToken)
	writerCtx := grpcAuthContext(writerToken)
	adminCtx := grpcAuthContext(adminToken)

	daemonCtx, cancelDaemon := context.WithTimeout(aliceCtx, 30*time.Second)
	defer cancelDaemon()
	stream, err := agent.Connect(daemonCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name:              "rpc-sec-allow-daemon",
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
	if _, err := slices.SetSliceCIDaemon(aliceCtx, &corev1.SetSliceCIDaemonRequest{Slice: sliceRef, DaemonId: daemonID}); err != nil {
		t.Fatalf("SetSliceCIDaemon: %v", err)
	}
	if _, err := slices.SetSliceSecret(aliceCtx, &corev1.SetSliceSecretRequest{
		Slice: sliceRef,
		Name:  "CI_TOKEN",
		Value: "rpc-secret-val",
	}); err != nil {
		t.Fatalf("SetSliceSecret: %v", err)
	}

	assertSecret := func(rc *corev1.RunChecks, want bool) {
		t.Helper()
		if len(rc.GetChecks()) != 1 {
			t.Fatalf("RunChecks checks len = %d, want 1", len(rc.GetChecks()))
		}
		got := rc.GetChecks()[0].GetEnv()["CI_TOKEN"]
		if want && got != "rpc-secret-val" {
			t.Fatalf("expected secret CI_TOKEN, got env=%v", rc.GetChecks()[0].GetEnv())
		}
		if !want && (got != "" || len(rc.GetChecks()[0].GetSecretEnv()) != 0) {
			t.Fatalf("expected secretless run, got env=%v secret_env=%v", rc.GetChecks()[0].GetEnv(), rc.GetChecks()[0].GetSecretEnv())
		}
	}

	// 1. Contributor patchset 1 initially runs secretless.
	csID, ps1 := createDirectPatchsetForSlice(t, contribCtx, clients, sliceRef, "/acme/payment/sec-allow-rpc/app.go", "package app\nconst V = 1\n", "rpc contrib ps1")
	msg1 := recvRunChecks(t, stream, csID, ps1)
	assertSecret(msg1, false)

	// Contributor denied AllowPatchsetSecrets.
	if _, err := checks.AllowPatchsetSecrets(contribCtx, &corev1.AllowPatchsetSecretsRequest{ChangesetId: csID}); grpcstatus.Code(err) != codes.PermissionDenied {
		t.Fatalf("contributor AllowPatchsetSecrets code = %v, want PermissionDenied", grpcstatus.Code(err))
	}

	// Writer allows ps1 -> rerun dispatches with CI_TOKEN.
	allow1, err := checks.AllowPatchsetSecrets(writerCtx, &corev1.AllowPatchsetSecretsRequest{ChangesetId: csID})
	if err != nil {
		t.Fatalf("writer AllowPatchsetSecrets: %v", err)
	}
	if allow1.GetPatchsetId() != ps1 || len(allow1.GetRerunChecks()) != 1 {
		t.Fatalf("allow1 = %+v", allow1)
	}
	msg2 := recvRunChecks(t, stream, csID, ps1)
	assertSecret(msg2, true)

	// 2. Patchset 2 pushed by contributor -> secretless again (ps1 allowance must NOT carry over).
	ref, err := clients.repository.GetRef(contribCtx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload2, err := clients.blob.UploadBlob(contribCtx, &corev1.UploadBlobRequest{
		Data:  []byte("package app\nconst V = 2\n"),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps2, err := clients.changeset.UpdateChangeset(contribCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  csID,
		BaseCommitId: ref.GetCommitId(),
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/sec-allow-rpc/app.go",
			BlobId:      upload2.GetBlobId(),
			ContentHash: upload2.GetContentHash(),
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg3 := recvRunChecks(t, stream, csID, ps2.GetId())
	assertSecret(msg3, false)

	// 3. Patchset 3 touching .gitslice/ requires account owner/admin (writer cannot allow secrets).
	upload3, err := clients.blob.UploadBlob(contribCtx, &corev1.UploadBlobRequest{
		Data:  []byte("version: 1\nchecks:\n  unit:\n    run: \"echo evil\"\n"),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps3, err := clients.changeset.UpdateChangeset(contribCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  csID,
		BaseCommitId: ref.GetCommitId(),
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/sec-allow-rpc/.gitslice/checks.yaml",
			BlobId:      upload3.GetBlobId(),
			ContentHash: upload3.GetContentHash(),
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg4 := recvRunChecks(t, stream, csID, ps3.GetId())
	assertSecret(msg4, false)

	if _, err := checks.AllowPatchsetSecrets(writerCtx, &corev1.AllowPatchsetSecretsRequest{ChangesetId: csID}); grpcstatus.Code(err) != codes.PermissionDenied {
		t.Fatalf("writer AllowPatchsetSecrets on .gitslice/ code = %v, want PermissionDenied", grpcstatus.Code(err))
	}

	// Account admin can allow secrets on .gitslice/ patchset 3.
	allow3, err := checks.AllowPatchsetSecrets(adminCtx, &corev1.AllowPatchsetSecretsRequest{ChangesetId: csID})
	if err != nil {
		t.Fatalf("account admin AllowPatchsetSecrets on .gitslice/: %v", err)
	}
	if allow3.GetPatchsetId() != ps3.GetId() || !strings.HasPrefix(allow3.GetAllowedBySubjectId(), "ext_") && allow3.GetAllowedBySubjectId() == "" {
		t.Fatalf("allow3 = %+v", allow3)
	}
	msg5 := recvRunChecks(t, stream, csID, ps3.GetId())
	assertSecret(msg5, true)
	_ = storage.DefaultTargetRef
}
