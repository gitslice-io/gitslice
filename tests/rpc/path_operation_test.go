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

func TestRPCPathOperationsRejectGitsliceAndCrossSliceIntoProtectedSlice(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	paymentRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/outside/unreviewed.txt", "outside\n", "seed outside")
	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/cfg/.gitslice/checks.yaml", "version: 1\nchecks: {}\n", "seed checks")
	protectedRef := createSubmitRequirementSlice(t, ctx, clients, slices, "protectedcopy", 1, []string{"unit"})

	// 1. Touching .gitslice directly or via a parent folder is always rejected with FailedPrecondition.
	_, err := clients.changeset.CopyPath(ctx, &corev1.CopyPathRequest{
		Slice:           paymentRef,
		SourcePath:      "/acme/payment/outside/unreviewed.txt",
		DestinationPath: "/acme/payment/.gitslice/checks.yaml",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	_, err = clients.changeset.MovePath(ctx, &corev1.MovePathRequest{
		Slice:           paymentRef,
		SourcePath:      "/acme/payment/cfg/.gitslice/checks.yaml",
		DestinationPath: "/acme/payment/outside/moved-checks.yaml",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	_, err = clients.changeset.CopyPath(ctx, &corev1.CopyPathRequest{
		Slice:           paymentRef,
		SourcePath:      "/acme/payment/cfg",
		DestinationPath: "/acme/payment/cfg-copy",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	// 2. Cross-slice CopyPath into a slice with required approvals/checks is rejected with FailedPrecondition.
	_, err = clients.changeset.CopyPath(ctx, &corev1.CopyPathRequest{
		Slice:           protectedRef,
		SourceSlice:     paymentRef,
		SourcePath:      "/acme/payment/outside/unreviewed.txt",
		DestinationPath: "/acme/payment/protectedcopy/unreviewed.txt",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	// 3. Copying or moving from an unprotected parent slice into the protected sub-slice is also rejected.
	_, err = clients.changeset.CopyPath(ctx, &corev1.CopyPathRequest{
		Slice:           paymentRef,
		SourcePath:      "/acme/payment/outside/unreviewed.txt",
		DestinationPath: "/acme/payment/protectedcopy/via-parent.txt",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)

	_, err = clients.changeset.MovePath(ctx, &corev1.MovePathRequest{
		Slice:           paymentRef,
		SourcePath:      "/acme/payment/outside/unreviewed.txt",
		DestinationPath: "/acme/payment/protectedcopy/moved-via-parent.txt",
	})
	assertGRPCCode(t, err, codes.FailedPrecondition)
}
