package rpc_test

import (
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestRPCCopyAndMovePathLandWithoutReview(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/copyreq/lib/a.txt", "alpha\n", "copy seed a")
	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/copyreq/lib/sub/b.txt", "beta\n", "copy seed b")
	sliceRef := createSubmitRequirementSlice(t, ctx, clients, slices, "copyreq", 1, []string{"unit"})

	copied, err := clients.changeset.CopyPath(ctx, &corev1.CopyPathRequest{
		Slice:           sliceRef,
		SourcePath:      "/acme/payment/copyreq/lib",
		DestinationPath: "/acme/payment/copyreq/vendor/lib",
	})
	if err != nil {
		t.Fatal(err)
	}
	if copied.GetCommitId() == "" || copied.GetChangeset().GetStatus() != "submitted" {
		t.Fatalf("copy response = %#v, want a landed commit", copied)
	}
	ts.waitForOutboxDrain(t)
	for path, want := range map[string]string{
		"/acme/payment/copyreq/lib/a.txt":            "alpha\n",
		"/acme/payment/copyreq/vendor/lib/a.txt":     "alpha\n",
		"/acme/payment/copyreq/vendor/lib/sub/b.txt": "beta\n",
	} {
		file, err := clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: copied.GetCommitId(), Path: path, Slice: sliceRef})
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if string(file.GetData()) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", path, file.GetData(), want)
		}
	}

	moved, err := clients.changeset.MovePath(ctx, &corev1.MovePathRequest{
		Slice:           sliceRef,
		SourcePath:      "/acme/payment/copyreq/vendor/lib/sub",
		DestinationPath: "/acme/payment/copyreq/vendor/sub",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: moved.GetCommitId(), Path: "/acme/payment/copyreq/vendor/sub/b.txt", Slice: sliceRef}); err != nil {
		t.Fatalf("moved file: %v", err)
	}
	_, err = clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: moved.GetCommitId(), Path: "/acme/payment/copyreq/vendor/lib/sub/b.txt", Slice: sliceRef})
	assertGRPCCode(t, err, codes.NotFound)
	// The original folder the copy came from is untouched by the move.
	if _, err := clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: moved.GetCommitId(), Path: "/acme/payment/copyreq/lib/sub/b.txt", Slice: sliceRef}); err != nil {
		t.Fatalf("copy source after the move: %v", err)
	}
	ts.waitForOutboxDrain(t)

	history, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment/copyreq/vendor/sub/b.txt",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, history.Commits, "Move /acme/payment/copyreq/vendor/lib/sub to /acme/payment/copyreq/vendor/sub")

	_, err = clients.changeset.CopyPath(ctx, &corev1.CopyPathRequest{
		Slice:           sliceRef,
		SourcePath:      "/acme/payment/copyreq/lib",
		DestinationPath: "/acme/payment/copyreq/vendor/lib",
	})
	assertGRPCCode(t, err, codes.AlreadyExists)
}
