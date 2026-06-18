package rpc_test

import (
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestStackedChangesetsChildUsesParentPreviewAndSubmitOrder(t *testing.T) {
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
	stack, err := clients.stack.CreateStack(ctx, &corev1.CreateStackRequest{
		AuthoringSlice: testPaymentSliceRef(),
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "stacked parser rollout",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stack.Status != "open" || stack.RootEntryId != "" {
		t.Fatalf("unexpected new stack: %#v", stack)
	}

	root, err := clients.stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId: stack.Id,
		Title:   "introduce parser",
	})
	if err != nil {
		t.Fatal(err)
	}
	rootBlob, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst StackValue = 1\n"),
		Slice: testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rootPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  root.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/stacked_shared.go",
			BlobId:      rootBlob.BlobId,
			ContentHash: rootBlob.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rootPatchset.BaseKind != "commit" || rootPatchset.BaseTreeId == "" || rootPatchset.ResultTreeId == "" {
		t.Fatalf("root patchset missing commit preview metadata: %#v", rootPatchset)
	}

	child, err := clients.stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId:           stack.Id,
		Title:             "use parser",
		ParentChangesetId: root.Id,
		ParentPatchsetId:  rootPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	childBlob, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst StackValue = 2\n"),
		Slice: testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	childPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:              child.Id,
		BaseCommitId:             ref.CommitId,
		BaseKind:                 "patchset",
		BasePatchsetId:           rootPatchset.Id,
		ExpectedParentPatchsetId: rootPatchset.Id,
		FileEdits: []*corev1.FileEdit{{
			Op:          "update",
			Path:        "/acme/payment/stacked_shared.go",
			BlobId:      childBlob.BlobId,
			ContentHash: childBlob.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if childPatchset.BaseKind != "patchset" || childPatchset.BasePatchsetId != rootPatchset.Id || childPatchset.BaseTreeId != rootPatchset.ResultTreeId || childPatchset.ResultTreeId == "" {
		t.Fatalf("child patchset missing parent-preview metadata: %#v", childPatchset)
	}

	diff, err := clients.changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{ChangesetId: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-const StackValue = 1") || !strings.Contains(diff.Diff, "+const StackValue = 2") {
		t.Fatalf("child diff did not use parent preview as old side:\n%s", diff.Diff)
	}

	_, err = clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               child.Id,
		ExpectedCurrentPatchsetId: childPatchset.Id,
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("child submit before parent err = %v, want FailedPrecondition", err)
	}
	blockedChild, err := clients.changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	if blockedChild.SubmitBlockedReason != "BlockedOnStackParent" {
		t.Fatalf("child blocked reason = %q, want BlockedOnStackParent", blockedChild.SubmitBlockedReason)
	}

	updatedStack, err := clients.stack.GetStack(ctx, &corev1.GetStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if updatedStack.RootEntryId != root.Id || updatedStack.ActiveEntryId != child.Id || len(updatedStack.Entries) != 2 {
		t.Fatalf("unexpected stack entries after child create: %#v", updatedStack)
	}
	if updatedStack.Entries[0].Depth != 0 || updatedStack.Entries[1].Depth != 1 || updatedStack.Entries[1].ParentChangesetId != root.Id {
		t.Fatalf("unexpected stack entry topology: %#v", updatedStack.Entries)
	}

	submitted, err := clients.stack.SubmitStack(ctx, &corev1.SubmitStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != "submitted" || len(submitted.Results) != 2 {
		t.Fatalf("unexpected submit stack response: %#v", submitted)
	}
	if submitted.Results[0].ChangesetId != root.Id || submitted.Results[0].Status != "pending_publish" {
		t.Fatalf("unexpected root submit result: %#v", submitted.Results[0])
	}
	if submitted.Results[1].ChangesetId != child.Id || submitted.Results[1].Status != "pending_publish" {
		t.Fatalf("unexpected child submit result: %#v", submitted.Results[1])
	}

	waitForSubmittedChangeset(t, ctx, clients.changeset, root.Id)
	waitForSubmittedChangeset(t, ctx, clients.changeset, child.Id)
	closedStack, err := clients.stack.GetStack(ctx, &corev1.GetStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if closedStack.Status != "closed" {
		t.Fatalf("stack status after publish = %q, want closed", closedStack.Status)
	}
	activeStacks, err := clients.stack.ListStacks(ctx, &corev1.ListStacksRequest{AuthoringSlice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range activeStacks.Stacks {
		if listed.Id == stack.Id {
			t.Fatalf("closed stack appeared in default list: %#v", activeStacks.Stacks)
		}
	}
}

func TestStackDetachEntryCreatesNewStack(t *testing.T) {
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
	stack, err := clients.stack.CreateStack(ctx, &corev1.CreateStackRequest{
		AuthoringSlice: testPaymentSliceRef(),
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "detach rpc stack",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := clients.stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId: stack.Id,
		Title:   "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	rootBlob, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst DetachRPCRoot = 1\n"),
		Slice: testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rootPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  root.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/detach_rpc_root.go",
			BlobId:      rootBlob.BlobId,
			ContentHash: rootBlob.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := clients.stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId:           stack.Id,
		Title:             "child",
		ParentChangesetId: root.Id,
		ParentPatchsetId:  rootPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	childBlob, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst DetachRPCChild = 1\n"),
		Slice: testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	childPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:              child.Id,
		BaseCommitId:             ref.CommitId,
		BaseKind:                 "patchset",
		BasePatchsetId:           rootPatchset.Id,
		ExpectedParentPatchsetId: rootPatchset.Id,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/detach_rpc_child.go",
			BlobId:      childBlob.BlobId,
			ContentHash: childBlob.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := clients.stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId:           stack.Id,
		Title:             "grandchild",
		ParentChangesetId: child.Id,
		ParentPatchsetId:  childPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	grandchildBlob, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst DetachRPCGrandchild = 1\n"),
		Slice: testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:              grandchild.Id,
		BaseCommitId:             ref.CommitId,
		BaseKind:                 "patchset",
		BasePatchsetId:           childPatchset.Id,
		ExpectedParentPatchsetId: childPatchset.Id,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/detach_rpc_grandchild.go",
			BlobId:      grandchildBlob.BlobId,
			ContentHash: grandchildBlob.ContentHash,
			Mode:        0o644,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = clients.stack.DetachStackEntry(ctx, &corev1.DetachStackEntryRequest{StackId: stack.Id, ChangesetId: root.Id})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("detach root err = %v, want InvalidArgument", err)
	}
	res, err := clients.stack.DetachStackEntry(ctx, &corev1.DetachStackEntryRequest{
		StackId:     stack.Id,
		ChangesetId: child.Id,
		Title:       "detached child rpc stack",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SourceStack == nil || res.DetachedStack == nil {
		t.Fatalf("detach response missing stacks: %#v", res)
	}
	if len(res.SourceStack.Entries) != 1 || res.SourceStack.RootEntryId != root.Id || res.SourceStack.ActiveEntryId != root.Id {
		t.Fatalf("unexpected source stack after detach: %#v", res.SourceStack)
	}
	if res.DetachedStack.Id == "" || res.DetachedStack.Id == stack.Id || res.DetachedStack.Title != "detached child rpc stack" {
		t.Fatalf("unexpected detached stack metadata: %#v", res.DetachedStack)
	}
	if len(res.DetachedStack.Entries) != 2 || res.DetachedStack.RootEntryId != child.Id || res.DetachedStack.ActiveEntryId != child.Id {
		t.Fatalf("unexpected detached stack topology: %#v", res.DetachedStack)
	}
	childEntry := rpcStackEntryForTest(t, res.DetachedStack, child.Id)
	if childEntry.ParentChangesetId != "" || childEntry.ParentPatchsetId != "" || childEntry.Depth != 0 || childEntry.State != "needs_restack" {
		t.Fatalf("unexpected detached child entry: %#v", childEntry)
	}
	grandchildEntry := rpcStackEntryForTest(t, res.DetachedStack, grandchild.Id)
	if grandchildEntry.ParentChangesetId != child.Id || grandchildEntry.ParentPatchsetId != childPatchset.Id || grandchildEntry.Depth != 1 || grandchildEntry.State != "needs_restack" {
		t.Fatalf("unexpected detached grandchild entry: %#v", grandchildEntry)
	}
	detachedChild, err := clients.changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	if detachedChild.StackId != res.DetachedStack.Id || detachedChild.ParentChangesetId != "" || detachedChild.ParentPatchsetId != "" || detachedChild.BaseKind != "commit" {
		t.Fatalf("unexpected detached child changeset: %#v", detachedChild)
	}
}

func rpcStackEntryForTest(t *testing.T, stack *corev1.ChangesetStack, changesetID string) *corev1.ChangesetStackEntry {
	t.Helper()
	for _, entry := range stack.Entries {
		if entry != nil && entry.ChangesetId == changesetID {
			return entry
		}
	}
	t.Fatalf("stack entry %s not found in %#v", changesetID, stack.Entries)
	return nil
}
