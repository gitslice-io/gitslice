package rpc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestRPCCommitHistoryFollowsExplicitFileMove(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/history/original.txt", "first\n", "history initial file")
	submitRPCFileEdits(t, ctx, clients, "payment", "history move file", []*corev1.FileEdit{{
		Op:      "rename",
		OldPath: "/acme/payment/history/original.txt",
		Path:    "/acme/payment/history/moved.txt",
	}})
	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/history/moved.txt", "second\n", "history edit moved file")
	ts.waitForOutboxDrain(t)

	follow, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment/history/moved.txt",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, follow.Commits, "history edit moved file", "history move file", "history initial file")

	noFollow := false
	literal, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName:     postgres.DefaultTargetRef,
		Path:        "/acme/payment/history/moved.txt",
		Limit:       10,
		FollowMoves: &noFollow,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, literal.Commits, "history edit moved file", "history move file")
	assertNoCommitMessage(t, literal.Commits, "history initial file")
}

func TestRPCCommitHistoryInfersExactDeleteAddMove(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	content := "same content\n"
	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/history/infer_old.txt", content, "history infer initial")
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	submitRPCFileEdits(t, ctx, clients, "payment", "history inferred move", []*corev1.FileEdit{
		{Op: "delete", Path: "/acme/payment/history/infer_old.txt"},
		{Op: "upsert", Path: "/acme/payment/history/infer_new.txt", BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644},
	})
	ts.waitForOutboxDrain(t)

	follow, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment/history/infer_new.txt",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, follow.Commits, "history inferred move", "history infer initial")

	noFollow := false
	literal, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName:     postgres.DefaultTargetRef,
		Path:        "/acme/payment/history/infer_new.txt",
		Limit:       10,
		FollowMoves: &noFollow,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, literal.Commits, "history inferred move")
	assertNoCommitMessage(t, literal.Commits, "history infer initial")
}

func TestRPCCommitHistoryIncludesAncestorDirectoryMove(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/history/dir/file.txt", "first\n", "history directory child initial")
	submitRPCFileEdits(t, ctx, clients, "payment", "history move directory", []*corev1.FileEdit{{
		Op:      "rename",
		OldPath: "/acme/payment/history/dir",
		Path:    "/acme/payment/history/renamed",
	}})
	ts.waitForOutboxDrain(t)

	follow, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment/history/renamed/file.txt",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, follow.Commits, "history move directory", "history directory child initial")

	noFollow := false
	literal, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName:     postgres.DefaultTargetRef,
		Path:        "/acme/payment/history/renamed/file.txt",
		Limit:       10,
		FollowMoves: &noFollow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(literal.Commits) != 0 {
		t.Fatalf("literal history for child path after directory move = %s, want none", commitMessageList(literal.Commits))
	}
}

func TestRPCCommitHistoryFollowsMoveWithinCustomSlice(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	oldPath := "/acme/payment/shared/history_custom_old.txt"
	newPath := "/acme/payment/shared/history_custom_new.txt"
	submitRPCFileWithTitle(t, ctx, clients, "backend", oldPath, "custom\n", "history custom initial")
	submitRPCFileEdits(t, ctx, clients, "backend", "history custom move", []*corev1.FileEdit{{
		Op:      "rename",
		OldPath: oldPath,
		Path:    newPath,
	}})
	ts.waitForOutboxDrain(t)

	follow, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    newPath,
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "backend"},
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, follow.Commits, "history custom move", "history custom initial")

	noFollow := false
	literal, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName:     postgres.DefaultTargetRef,
		Path:        newPath,
		Slice:       &corev1.SliceRef{Account: "acme", Slice: "backend"},
		Limit:       10,
		FollowMoves: &noFollow,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, literal.Commits, "history custom move")
	assertNoCommitMessage(t, literal.Commits, "history custom initial")
}

func TestRPCCommitHistoryDoesNotInferAmbiguousExactMove(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	content := "ambiguous\n"
	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/history/ambiguous_a.txt", content, "history ambiguous a")
	submitRPCFileWithTitle(t, ctx, clients, "payment", "/acme/payment/history/ambiguous_b.txt", content, "history ambiguous b")
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	submitRPCFileEdits(t, ctx, clients, "payment", "history ambiguous delete add", []*corev1.FileEdit{
		{Op: "delete", Path: "/acme/payment/history/ambiguous_a.txt"},
		{Op: "delete", Path: "/acme/payment/history/ambiguous_b.txt"},
		{Op: "upsert", Path: "/acme/payment/history/ambiguous_new.txt", BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644},
	})
	ts.waitForOutboxDrain(t)

	follow, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment/history/ambiguous_new.txt",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, follow.Commits, "history ambiguous delete add")
	assertNoCommitMessage(t, follow.Commits, "history ambiguous a")
	assertNoCommitMessage(t, follow.Commits, "history ambiguous b")
}

func TestRPCCommitHistoryDeleteRecreateSamePathStartsNewEntity(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	path := "/acme/payment/history/recreated.txt"
	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "old\n", "history recreate old")
	submitRPCFileEdits(t, ctx, clients, "payment", "history recreate delete", []*corev1.FileEdit{{Op: "delete", Path: path}})
	submitRPCFileWithTitle(t, ctx, clients, "payment", path, "new\n", "history recreate new")
	ts.waitForOutboxDrain(t)

	follow, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    path,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, follow.Commits, "history recreate new")
	assertNoCommitMessage(t, follow.Commits, "history recreate old")
	assertNoCommitMessage(t, follow.Commits, "history recreate delete")

	noFollow := false
	literal, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName:     postgres.DefaultTargetRef,
		Path:        path,
		Limit:       10,
		FollowMoves: &noFollow,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitMessages(t, literal.Commits, "history recreate new", "history recreate delete", "history recreate old")
}

func submitRPCFileWithTitle(t *testing.T, ctx context.Context, clients testCoreClients, sliceName, path, content, title string) string {
	t.Helper()
	// Upload through the authoring slice: UpdateChangeset only accepts content
	// hashes already accessible to the slice the changeset is authored with.
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: &corev1.SliceRef{Account: "acme", Slice: sliceName}})
	if err != nil {
		t.Fatal(err)
	}
	return submitRPCFileEdits(t, ctx, clients, sliceName, title, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        path,
		BlobId:      upload.BlobId,
		ContentHash: upload.ContentHash,
		Mode:        0o100644,
	}})
}

func submitRPCFileEdits(t *testing.T, ctx context.Context, clients testCoreClients, sliceName, title string, edits []*corev1.FileEdit) string {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: sliceName},
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
		FileEdits:    edits,
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
	return waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id).CommitId
}

func assertCommitMessages(t *testing.T, commits []*corev1.Commit, want ...string) {
	t.Helper()
	if len(commits) < len(want) {
		t.Fatalf("commit messages = %s, want prefix %s", commitMessageList(commits), strings.Join(want, ", "))
	}
	for i, message := range want {
		if commits[i].Message != message {
			t.Fatalf("commit[%d] message = %q, want %q; all messages: %s", i, commits[i].Message, message, commitMessageList(commits))
		}
	}
}

func assertNoCommitMessage(t *testing.T, commits []*corev1.Commit, message string) {
	t.Helper()
	for _, commit := range commits {
		if commit.Message == message {
			t.Fatalf("unexpected message %q in commits: %s", message, commitMessageList(commits))
		}
	}
}

func commitMessageList(commits []*corev1.Commit) string {
	messages := make([]string, 0, len(commits))
	for _, commit := range commits {
		messages = append(messages, commit.Message)
	}
	return strings.Join(messages, ", ")
}
