package rpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestChangesetServiceListAndDiff(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\nconst Version = 1\n"), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "rpc diff workflow",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/rpc_diff.go",
			BlobId:      first.BlobId,
			ContentHash: first.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\nconst Version = 2\n"), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	secondPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: firstPatchset.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/rpc_diff.go",
			BlobId:      second.BlobId,
			ContentHash: second.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := clients.changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Status:         "draft",
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changesetListContains(listed.Changesets, cs.Id) {
		t.Fatalf("ListChangesets did not include %s: %#v", cs.Id, listed.Changesets)
	}

	firstDiff, err := clients.changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstDiff.ToPatchsetId != firstPatchset.Id || !containsString(firstDiff.ChangedPaths, "/acme/payment/rpc_diff.go") || !strings.Contains(firstDiff.Diff, "+const Version = 1") {
		t.Fatalf("unexpected first patchset diff: %#v", firstDiff)
	}

	between, err := clients.changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId:  cs.Id,
		FromPatchset: "1",
		ToPatchset:   "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if between.FromPatchsetId != firstPatchset.Id || between.ToPatchsetId != secondPatchset.Id {
		t.Fatalf("unexpected patchset ids in diff: %#v", between)
	}
	if !strings.Contains(between.Diff, "-const Version = 1") || !strings.Contains(between.Diff, "+const Version = 2") {
		t.Fatalf("patchset-to-patchset diff missing expected content:\n%s", between.Diff)
	}
}

func TestUpdateChangesetDeduplicatesIdenticalPatchset(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	firstBlob, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst Dedup = 1\n"),
		Slice: testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "rpc patchset dedup",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstEdit := &corev1.FileEdit{
		Op:          "upsert",
		Path:        "/acme/payment/rpc_dedup.go",
		BlobId:      firstBlob.BlobId,
		ContentHash: firstBlob.ContentHash,
		Mode:        0o644,
	}
	firstPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{firstEdit},
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicatePatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: firstPatchset.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits:                 []*corev1.FileEdit{firstEdit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicatePatchset.Id != firstPatchset.Id || duplicatePatchset.Number != firstPatchset.Number {
		t.Fatalf("duplicate patchset = (%q, %d), want (%q, %d)", duplicatePatchset.Id, duplicatePatchset.Number, firstPatchset.Id, firstPatchset.Number)
	}

	secondBlob, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst Dedup = 2\n"),
		Slice: testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	changedPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: duplicatePatchset.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/rpc_dedup.go",
			BlobId:      secondBlob.BlobId,
			ContentHash: secondBlob.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changedPatchset.Id == firstPatchset.Id || changedPatchset.Number != firstPatchset.Number+1 {
		t.Fatalf("changed patchset = (%q, %d), want new patchset number %d", changedPatchset.Id, changedPatchset.Number, firstPatchset.Number+1)
	}
}

func createDirectPatchset(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
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
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs.Id, patchset.Id
}

func submitDirectFile(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) *corev1.Changeset {
	t.Helper()
	changesetID, patchsetID := createDirectPatchset(t, ctx, clients, path, content, title)
	submitted, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != "pending_publish" && submitted.Status != "submitted" {
		t.Fatalf("unexpected submit response: %#v", submitted)
	}
	return waitForSubmittedChangeset(t, ctx, clients.changeset, changesetID)
}

func waitForSubmittedChangeset(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient, changesetID string) *corev1.Changeset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *corev1.Changeset
	for time.Now().Before(deadline) {
		cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			t.Fatal(err)
		}
		last = cs
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach submitted status, last=%#v", changesetID, last)
	return nil
}

func treeEntriesContainName(entries []*corev1.TreeEntry, want string) bool {
	for _, entry := range entries {
		if entry != nil && entry.Name == want {
			return true
		}
	}
	return false
}

func changesetListContains(values []*corev1.Changeset, want string) bool {
	for _, value := range values {
		if value != nil && value.Id == want {
			return true
		}
	}
	return false
}
