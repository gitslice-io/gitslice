package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/storage/memory"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestServicesRunAgainstInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("hello\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := handlers.Blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{uploaded.ContentHash, "sha256:missing"}, Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Blobs) != 2 || status.Blobs[0].State != "available" || status.Blobs[1].State != "missing" {
		t.Fatalf("unexpected blob status: %#v", status.Blobs)
	}

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "add note",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/notes.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(patchset.PathBases) != 1 || patchset.PathBases[0].Exists {
		t.Fatalf("unexpected patchset path bases: %#v", patchset.PathBases)
	}
	if patchset.ResultTreeId == "" {
		t.Fatal("patchset result tree id is empty")
	}
	previewRead, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{RootTreeId: patchset.ResultTreeId, Path: "/acme/notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if string(previewRead.Data) != "hello\n" {
		t.Fatalf("preview read data = %q", string(previewRead.Data))
	}
	previewListed, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{RootTreeId: patchset.ResultTreeId, Path: "/acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(previewListed.Entries) != 1 || previewListed.Entries[0].Name != "notes.txt" {
		t.Fatalf("unexpected preview directory entries: %#v", previewListed.Entries)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if published, err := mem.Changesets.PublishPending(ctx, 10); err != nil || published != 1 {
		t.Fatalf("PublishPending = %d, %v; want 1, nil", published, err)
	}

	ref, err = handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != "hello\n" {
		t.Fatalf("read data = %q", string(read.Data))
	}
	listed, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "notes.txt" {
		t.Fatalf("unexpected directory entries: %#v", listed.Entries)
	}

	state, err := handlers.Workspace.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: &corev1.WorkspaceRef{Id: "acme/home"}})
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitId != ref.CommitId || state.Slice.SliceId == "" {
		t.Fatalf("unexpected workspace state: %#v", state)
	}
}

func TestBundledCheckRunNonTerminalStatusRecordsErrored(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "bundled-invalid"}
	mem.PutSliceWithSubmitSettings(sliceRef, []string{"/acme/payment/bundled-invalid"}, "private", 0, []string{"unit"})

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package bundledinvalid\nconst V = 1\n"),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "bundled invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/bundled-invalid/change.go",
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
		BundledCheckRuns: []*corev1.BundledCheckRun{{
			Name:   "unit",
			Status: "running",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	runs, err := handlers.Check.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id, PatchsetId: patchset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs.Runs) != 1 {
		t.Fatalf("ListCheckRuns returned %d runs, want 1: %#v", len(runs.Runs), runs.Runs)
	}
	if got := runs.Runs[0]; got.Status != "errored" || got.Summary != `invalid bundled check status "running"` {
		t.Fatalf("bundled run = %#v, want errored invalid-status summary", got)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err == nil || !strings.Contains(err.Error(), `required check "unit" is failing`) {
		t.Fatalf("SubmitChangeset error = %v, want failing unit check", err)
	}
}

func TestSubmitRejectsPatchsetConflicts(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "conflicted sync",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		PatchsetKind: "sync",
		Conflicts: []*corev1.PatchsetConflict{{
			Path:          "/acme/conflict.txt",
			ConflictClass: "content",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "unresolved patchset conflicts") {
		t.Fatalf("SubmitChangeset error = %v, want unresolved conflict FailedPrecondition", err)
	}
}

func TestStackedChangesetsUseParentPreviewInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := handlers.Stack.CreateStack(ctx, &corev1.CreateStackRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "memory stack",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := handlers.Stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId: stack.Id,
		Title:   "root entry",
	})
	if err != nil {
		t.Fatal(err)
	}
	rootBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst StackMemory = 1\n"),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootPatchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  root.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/stack_memory.go",
			BlobId:      rootBlob.BlobId,
			ContentHash: rootBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rootPatchset.BaseKind != "commit" || rootPatchset.BaseTreeId == "" || rootPatchset.ResultTreeId == "" {
		t.Fatalf("root patchset preview metadata = %#v", rootPatchset)
	}

	child, err := handlers.Stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId:           stack.Id,
		Title:             "child entry",
		ParentChangesetId: root.Id,
		ParentPatchsetId:  rootPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	childBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst StackMemory = 2\n"),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	childPatchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:              child.Id,
		BaseCommitId:             ref.CommitId,
		BaseKind:                 "patchset",
		BasePatchsetId:           rootPatchset.Id,
		ExpectedParentPatchsetId: rootPatchset.Id,
		FileEdits: []*corev1.FileEdit{{
			Op:          "update",
			Path:        "/acme/payment/stack_memory.go",
			BlobId:      childBlob.BlobId,
			ContentHash: childBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if childPatchset.BaseKind != "patchset" || childPatchset.BaseTreeId != rootPatchset.ResultTreeId {
		t.Fatalf("child patchset parent metadata = %#v", childPatchset)
	}
	if len(childPatchset.PathBases) != 1 || !childPatchset.PathBases[0].Exists || childPatchset.PathBases[0].ContentHash != rootBlob.ContentHash {
		t.Fatalf("child path base did not come from parent preview: %#v", childPatchset.PathBases)
	}
	diff, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{ChangesetId: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-const StackMemory = 1") || !strings.Contains(diff.Diff, "+const StackMemory = 2") {
		t.Fatalf("child diff did not use parent preview:\n%s", diff.Diff)
	}
	_, err = handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               child.Id,
		ExpectedCurrentPatchsetId: childPatchset.Id,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("child submit before parent err = %v, want FailedPrecondition", err)
	}
	blockedChild, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	if blockedChild.SubmitBlockedReason != "BlockedOnBaseChangeset" {
		t.Fatalf("blocked reason = %q, want BlockedOnBaseChangeset", blockedChild.SubmitBlockedReason)
	}
	submitted, err := handlers.Stack.SubmitStack(ctx, &corev1.SubmitStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != "submitted" || len(submitted.Results) != 2 {
		t.Fatalf("unexpected stack submit response: %#v", submitted)
	}
	if submitted.Results[0].ChangesetId != root.Id || submitted.Results[1].ChangesetId != child.Id {
		t.Fatalf("stack submit order = %#v, want root then child", submitted.Results)
	}
	if published, err := mem.Changesets.PublishPending(ctx, 10); err != nil || published != 2 {
		t.Fatalf("PublishPending = %d, %v; want 2, nil", published, err)
	}
	closedStack, err := handlers.Stack.GetStack(ctx, &corev1.GetStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if closedStack.Status != "closed" {
		t.Fatalf("stack status after publish = %q, want closed", closedStack.Status)
	}
	activeStacks, err := handlers.Stack.ListStacks(ctx, &corev1.ListStacksRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range activeStacks.Stacks {
		if listed.Id == stack.Id {
			t.Fatalf("closed stack appeared in default list: %#v", activeStacks.Stacks)
		}
	}
	closedStacks, err := handlers.Stack.ListStacks(ctx, &corev1.ListStacksRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Status:         "closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(closedStacks.Stacks) != 1 || closedStacks.Stacks[0].Id != stack.Id {
		t.Fatalf("closed stack list = %#v, want stack %s", closedStacks.Stacks, stack.Id)
	}
}

func TestSubmitStackMarksPartialStatusInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := handlers.Stack.CreateStack(ctx, &corev1.CreateStackRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "partial stack",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, rootPatchset := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, "", "", ref.CommitId, "root", "/acme/payment/partial.go", "package payment\nconst Partial = 1\n")
	child, err := handlers.Stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId:           stack.Id,
		Title:             "child without patchset",
		ParentChangesetId: root.Id,
		ParentPatchsetId:  rootPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	submitted, err := handlers.Stack.SubmitStack(ctx, &corev1.SubmitStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != "partial" || len(submitted.Results) != 2 {
		t.Fatalf("unexpected partial submit response: %#v", submitted)
	}
	if submitted.Results[0].ChangesetId != root.Id || submitted.Results[0].Status != "pending_publish" {
		t.Fatalf("unexpected root submit result: %#v", submitted.Results[0])
	}
	if submitted.Results[1].ChangesetId != child.Id || submitted.Results[1].Status != "blocked" {
		t.Fatalf("unexpected child submit result: %#v", submitted.Results[1])
	}
	partialStack, err := handlers.Stack.GetStack(ctx, &corev1.GetStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	if partialStack.Status != "partial" {
		t.Fatalf("stack status = %q, want partial", partialStack.Status)
	}
	partialStacks, err := handlers.Stack.ListStacks(ctx, &corev1.ListStacksRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Status:         "partial",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(partialStacks.Stacks) != 1 || partialStacks.Stacks[0].Id != stack.Id {
		t.Fatalf("partial stack list = %#v, want stack %s", partialStacks.Stacks, stack.Id)
	}
}

func TestStackValidationRejectsMismatchedSliceStaleParentAndParentAbandon(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := handlers.Stack.CreateStack(ctx, &corev1.CreateStackRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "validation stack",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "wrong slice entry",
		StackId:        stack.Id,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("mismatched stack slice err = %v, want InvalidArgument", err)
	}

	root, rootPatchset := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, "", "", ref.CommitId, "root", "/acme/payment/validation_root.go", "package payment\nconst ValidationRoot = 1\n")
	child, err := handlers.Stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId:           stack.Id,
		Title:             "child",
		ParentChangesetId: root.Id,
		ParentPatchsetId:  rootPatchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	nextRootBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst ValidationRoot = 2\n"),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               root.Id,
		ExpectedCurrentPatchsetId: rootPatchset.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/validation_root.go",
			BlobId:      nextRootBlob.BlobId,
			ContentHash: nextRootBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	childBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst ValidationChild = 1\n"),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:              child.Id,
		BaseCommitId:             ref.CommitId,
		BaseKind:                 "patchset",
		ExpectedParentPatchsetId: rootPatchset.Id,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/validation_child.go",
			BlobId:      childBlob.BlobId,
			ContentHash: childBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("stale expected parent patchset err = %v, want Aborted", err)
	}

	_, err = handlers.Changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: root.Id})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("abandon parent err = %v, want FailedPrecondition", err)
	}
}

func TestInMemoryPublishPendingPreservesAdmissionOrder(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	older, olderPatchset := createStandaloneChangesetWithPatchset(t, ctx, handlers, ref.CommitId, "older entry", "/acme/payment/order_older.go", "package payment\nconst OrderOlder = 1\n")
	newer, newerPatchset := createStandaloneChangesetWithPatchset(t, ctx, handlers, ref.CommitId, "newer entry", "/acme/payment/order_newer.go", "package payment\nconst OrderNewer = 1\n")

	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               newer.Id,
		ExpectedCurrentPatchsetId: newerPatchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               older.Id,
		ExpectedCurrentPatchsetId: olderPatchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if published, err := mem.Changesets.PublishPending(ctx, 10); err != nil || published != 2 {
		t.Fatalf("PublishPending = %d, %v; want 2, nil", published, err)
	}

	currentRef, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	finalCommit, err := handlers.Repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: currentRef.CommitId})
	if err != nil {
		t.Fatal(err)
	}
	if finalCommit.Message != "older entry" || len(finalCommit.ParentIds) != 1 {
		t.Fatalf("final commit = %#v, want older entry on top of admission chain", finalCommit)
	}
	parentCommit, err := handlers.Repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: finalCommit.ParentIds[0]})
	if err != nil {
		t.Fatal(err)
	}
	if parentCommit.Message != "newer entry" || len(parentCommit.ParentIds) != 1 || parentCommit.ParentIds[0] != ref.CommitId {
		t.Fatalf("parent commit = %#v, want newer entry first after base %s", parentCommit, ref.CommitId)
	}
}

func TestStackMutationAndRestackUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := handlers.Stack.CreateStack(ctx, &corev1.CreateStackRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "mutation stack",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, rootPatchset := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, "", "", ref.CommitId, "root", "/acme/payment/restack_base.go", "package payment\nconst RestackBase = 1\n")
	child, childPatchset := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, root.Id, rootPatchset.Id, ref.CommitId, "child", "/acme/payment/restack_base.go", "package payment\nconst RestackBase = 2\n")
	sibling, siblingPatchset := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, root.Id, rootPatchset.Id, ref.CommitId, "sibling", "/acme/payment/restack_parent.go", "package payment\nconst RestackParent = true\n")

	moved, err := handlers.Stack.MoveStackEntry(ctx, &corev1.MoveStackEntryRequest{
		StackId:      stack.Id,
		ChangesetId:  sibling.Id,
		SiblingOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved.Entries) != 3 || moved.Entries[1].ChangesetId != sibling.Id || moved.Entries[2].ChangesetId != child.Id {
		t.Fatalf("unexpected moved stack order: %#v", moved.Entries)
	}

	_, err = handlers.Stack.ReparentStackEntry(ctx, &corev1.ReparentStackEntryRequest{
		StackId:              stack.Id,
		ChangesetId:          root.Id,
		NewParentChangesetId: child.Id,
		NewParentPatchsetId:  childPatchset.Id,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("cycle reparent err = %v, want FailedPrecondition", err)
	}

	reparented, err := handlers.Stack.ReparentStackEntry(ctx, &corev1.ReparentStackEntryRequest{
		StackId:              stack.Id,
		ChangesetId:          child.Id,
		NewParentChangesetId: sibling.Id,
		NewParentPatchsetId:  siblingPatchset.Id,
		SiblingOrder:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	childEntry := stackEntryForTest(t, reparented, child.Id)
	if childEntry.ParentChangesetId != sibling.Id || childEntry.ParentPatchsetId != siblingPatchset.Id || childEntry.State != "needs_restack" {
		t.Fatalf("unexpected reparented child entry: %#v", childEntry)
	}

	restacked, err := handlers.Stack.Restack(ctx, &corev1.RestackRequest{
		StackId:          stack.Id,
		StartChangesetId: child.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restacked.Status != "clean" || len(restacked.Entries) != 1 {
		t.Fatalf("unexpected restack response: %#v", restacked)
	}
	restackedChild, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	if restackedChild.CurrentPatchsetNumber != childPatchset.Number+1 {
		t.Fatalf("child patchset number = %d, want %d", restackedChild.CurrentPatchsetNumber, childPatchset.Number+1)
	}
	current := currentPatchset(restackedChild)
	if current == nil || current.BasePatchsetId != siblingPatchset.Id || current.BaseTreeId != siblingPatchset.ResultTreeId {
		t.Fatalf("restacked child current patchset = %#v", current)
	}
	finalStack, err := handlers.Stack.GetStack(ctx, &corev1.GetStackRequest{StackId: stack.Id})
	if err != nil {
		t.Fatal(err)
	}
	finalChildEntry := stackEntryForTest(t, finalStack, child.Id)
	if finalChildEntry.State != "draft" {
		t.Fatalf("restacked child state = %q, want draft", finalChildEntry.State)
	}
}

func TestDetachStackEntryMovesSubtreeInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := handlers.Stack.CreateStack(ctx, &corev1.CreateStackRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "source stack",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, rootPatchset := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, "", "", ref.CommitId, "root", "/acme/payment/detach_root.go", "package payment\nconst DetachRoot = 1\n")
	child, childPatchset := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, root.Id, rootPatchset.Id, ref.CommitId, "child", "/acme/payment/detach_child.go", "package payment\nconst DetachChild = 1\n")
	grandchild, _ := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, child.Id, childPatchset.Id, ref.CommitId, "grandchild", "/acme/payment/detach_grandchild.go", "package payment\nconst DetachGrandchild = 1\n")
	sibling, _ := createStackEntryWithPatchset(t, ctx, handlers, stack.Id, root.Id, rootPatchset.Id, ref.CommitId, "sibling", "/acme/payment/detach_sibling.go", "package payment\nconst DetachSibling = 1\n")

	_, err = handlers.Stack.DetachStackEntry(ctx, &corev1.DetachStackEntryRequest{StackId: stack.Id, ChangesetId: root.Id})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("detach root err = %v, want InvalidArgument", err)
	}

	res, err := handlers.Stack.DetachStackEntry(ctx, &corev1.DetachStackEntryRequest{
		StackId:     stack.Id,
		ChangesetId: child.Id,
		Title:       "detached child stack",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SourceStack == nil || res.DetachedStack == nil {
		t.Fatalf("detach response missing stacks: %#v", res)
	}
	if res.DetachedStack.Id == "" || res.DetachedStack.Id == stack.Id {
		t.Fatalf("unexpected detached stack id: %q", res.DetachedStack.Id)
	}
	if res.DetachedStack.Title != "detached child stack" || res.DetachedStack.BaseCommitId != stack.BaseCommitId {
		t.Fatalf("unexpected detached stack metadata: %#v", res.DetachedStack)
	}
	if len(res.SourceStack.Entries) != 2 || !stackContainsEntry(res.SourceStack, root.Id) || !stackContainsEntry(res.SourceStack, sibling.Id) || stackContainsEntry(res.SourceStack, child.Id) {
		t.Fatalf("unexpected source stack entries after detach: %#v", res.SourceStack.Entries)
	}
	if len(res.DetachedStack.Entries) != 2 || res.DetachedStack.RootEntryId != child.Id || res.DetachedStack.ActiveEntryId != child.Id {
		t.Fatalf("unexpected detached stack topology: %#v", res.DetachedStack)
	}
	childEntry := stackEntryForTest(t, res.DetachedStack, child.Id)
	if childEntry.StackId != res.DetachedStack.Id || childEntry.ParentChangesetId != "" || childEntry.ParentPatchsetId != "" || childEntry.Depth != 0 || childEntry.State != "needs_restack" {
		t.Fatalf("unexpected detached child entry: %#v", childEntry)
	}
	grandchildEntry := stackEntryForTest(t, res.DetachedStack, grandchild.Id)
	if grandchildEntry.StackId != res.DetachedStack.Id || grandchildEntry.ParentChangesetId != child.Id || grandchildEntry.ParentPatchsetId != childPatchset.Id || grandchildEntry.Depth != 1 || grandchildEntry.State != "needs_restack" {
		t.Fatalf("unexpected detached grandchild entry: %#v", grandchildEntry)
	}
	detachedChild, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	if detachedChild.StackId != res.DetachedStack.Id || detachedChild.ParentChangesetId != "" || detachedChild.ParentPatchsetId != "" || detachedChild.BaseKind != "commit" {
		t.Fatalf("unexpected detached child changeset: %#v", detachedChild)
	}
}

func TestRestackConflictsFromEditsRecordsAttemptedPaths(t *testing.T) {
	cs := &corev1.Changeset{BaseCommitId: "cmt_old"}
	conflicts := restackConflictsFromEdits(cs, []*corev1.FileEdit{
		{Op: "rename", OldPath: "/acme/payment/old.go", Path: "/acme/payment/new.go"},
		{Op: "upsert", Path: "/acme/payment/new.go", ContentHash: "sha256:local"},
	}, "cmt_new")
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %#v, want two unique paths", conflicts)
	}
	paths := map[string]bool{}
	for _, conflict := range conflicts {
		paths[conflict.Path] = true
		if conflict.ConflictClass != "restack" || conflict.OldBaseCommitId != "cmt_old" || conflict.NewBaseCommitId != "cmt_new" {
			t.Fatalf("unexpected conflict metadata: %#v", conflict)
		}
	}
	if !paths["/acme/payment/old.go"] || !paths["/acme/payment/new.go"] {
		t.Fatalf("unexpected conflict paths: %#v", conflicts)
	}
	for _, conflict := range conflicts {
		if conflict.Path == "/acme/payment/new.go" && conflict.LocalContentHash != "sha256:local" {
			t.Fatalf("new path local content hash = %q, want sha256:local", conflict.LocalContentHash)
		}
	}
}

func TestSliceServiceUsesInMemoryRepositoryValidation(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	_, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "missing"},
		IncludedPaths: []string{"/acme/missing"},
		Visibility:    "private",
	})
	if err == nil || !strings.Contains(err.Error(), "included path does not exist") {
		t.Fatalf("CreateSlice missing path error = %v", err)
	}

	data := []byte("package payment\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_payment", []storage.FileEntry{{
		Path:        "/acme/payment/main.go",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/payment/main.go"})

	slice, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "payment"},
		IncludedPaths: []string{"/acme/payment"},
		Visibility:    "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	if slice.Ref.Account != "acme" || slice.Ref.Slice != "payment" {
		t.Fatalf("unexpected slice: %#v", slice)
	}
}

func TestChangesetDiffListAndAbandonUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	oldData := []byte("old\n")
	oldHash := objectid.RawContentHash(oldData)
	mem.PutObject(filesystem.BlobKey(oldHash), oldData)
	mem.PutCommitWithFiles("commit_old", []storage.FileEntry{{
		Path:        "/acme/payment/value.txt",
		BlobID:      objectid.BlobID(oldData),
		ContentHash: oldHash,
		Mode:        0o100644,
		Size:        int64(len(oldData)),
	}}, []string{"/acme/payment/value.txt"})
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	newBlob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("new\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "update value",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/value.txt",
			BlobId:      newBlob.BlobId,
			ContentHash: newBlob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{ChangesetId: cs.Id, Patchset: patchset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-old") || !strings.Contains(diff.Diff, "+new") {
		t.Fatalf("diff did not include old/new content:\n%s", diff.Diff)
	}
	listed, err := handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Changesets) != 1 || listed.Changesets[0].Id != cs.Id {
		t.Fatalf("unexpected changeset list: %#v", listed.Changesets)
	}
	if _, err := handlers.Changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	abandoned, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.Status != "abandoned" {
		t.Fatalf("status = %q, want abandoned", abandoned.Status)
	}
}

func TestSliceCRUDAndCommitHistoryUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	data := []byte("package api\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_api", []storage.FileEntry{{
		Path:        "/acme/payment/api/main.go",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/payment/api/main.go"})

	slice, err := handlers.Slice.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "api"},
		IncludedPaths: []string{"/acme/payment"},
		Visibility:    "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := handlers.Slice.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !sliceListContains(listed.Slices, "api") {
		t.Fatalf("ListSlices did not include api: %#v", listed.Slices)
	}
	definition, err := handlers.Slice.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment/api"},
			Visibility:    "private",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(definition.IncludedPaths, ",") != "/acme/payment/api" {
		t.Fatalf("updated included paths = %#v", definition.IncludedPaths)
	}
	definitionHistory, err := handlers.Slice.ListSliceDefinitionVersions(ctx, &corev1.ListSliceDefinitionVersionsRequest{SliceId: slice.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitionHistory.Versions) != 2 || definitionHistory.Versions[0].Version != 2 || definitionHistory.Versions[1].Version != 1 {
		t.Fatalf("unexpected definition history: %#v", definitionHistory.Versions)
	}
	for _, version := range definitionHistory.Versions {
		if version.CreatedBy != "user_alice" || version.CreatedAt == "" {
			t.Fatalf("unexpected definition history metadata: %#v", version)
		}
	}
	history, err := handlers.Repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		Path:  "/acme/payment/api",
		Slice: &corev1.SliceRef{Account: "acme", Slice: "api"},
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Commits) != 1 || history.Commits[0].Id != "commit_api" {
		t.Fatalf("unexpected commit history: %#v", history.Commits)
	}
	if _, err := handlers.Slice.DeleteSlice(ctx, &corev1.DeleteSliceRequest{SliceId: slice.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: slice.Id}); err == nil {
		t.Fatalf("GetSlice after delete succeeded")
	}
}

func TestChooseUsernameCreatesHomeSliceInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), "clerk_nic", "nic@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	chosen, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "nic"})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.SubjectId != subjectID || chosen.Account != "nic" {
		t.Fatalf("unexpected choose-username response: %#v", chosen)
	}
	slice, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "nic", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(slice.Definition.IncludedPaths, ",") != "/nic" {
		t.Fatalf("home included paths = %#v", slice.Definition.IncludedPaths)
	}
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{
		CommitId: ref.CommitId,
		Path:     "/nic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("home account root entry = %#v, want directory", resolved.Entry)
	}
}

func TestChangesetAuthorsResolveToPersonalUsernameInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), "clerk_taylor", "taylor@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	if _, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "Taylor_Name"}); err != nil {
		t.Fatal(err)
	}

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "taylor-name", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "show username",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.Author != "taylor-name" {
		t.Fatalf("CreateChangeset author = %q, want taylor-name", cs.Author)
	}

	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("hello\n"),
		Slice: &corev1.SliceRef{Account: "taylor-name", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/taylor-name/hello.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if patchset.Author != "taylor-name" {
		t.Fatalf("UpdateChangeset patchset author = %q, want taylor-name", patchset.Author)
	}

	got, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Author != "taylor-name" {
		t.Fatalf("GetChangeset author = %q, want taylor-name", got.Author)
	}
	if len(got.Patchsets) != 1 || got.Patchsets[0].Author != "taylor-name" {
		t.Fatalf("GetChangeset patchset authors = %#v, want taylor-name", got.Patchsets)
	}

	listed, err := handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "taylor-name", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Changesets) != 1 || listed.Changesets[0].Author != "taylor-name" {
		t.Fatalf("ListChangesets authors = %#v, want taylor-name", listed.Changesets)
	}
	if len(listed.Changesets[0].Patchsets) != 1 || listed.Changesets[0].Patchsets[0].Author != "taylor-name" {
		t.Fatalf("ListChangesets patchset authors = %#v, want taylor-name", listed.Changesets[0].Patchsets)
	}

	unresolvedCtx := authctx.WithSubjectID(context.Background(), "user_alice")
	unresolved, err := handlers.Changeset.CreateChangeset(unresolvedCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "fallback author",
	})
	if err != nil {
		t.Fatal(err)
	}
	if unresolved.Author != "user_alice" {
		t.Fatalf("unresolved CreateChangeset author = %q, want user_alice", unresolved.Author)
	}
	unresolvedGot, err := handlers.Changeset.GetChangeset(unresolvedCtx, &corev1.GetChangesetRequest{ChangesetId: unresolved.Id})
	if err != nil {
		t.Fatal(err)
	}
	if unresolvedGot.Author != "user_alice" {
		t.Fatalf("unresolved GetChangeset author = %q, want user_alice", unresolvedGot.Author)
	}
}

func TestCommitAuthorsResolveToPersonalUsername(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), "clerk_riley", "riley@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	if _, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "riley"}); err != nil {
		t.Fatal(err)
	}

	// A commit authored by a known subject resolves to the username; an unknown
	// author (no personal account) is left as the raw subject id.
	authored := &corev1.Commit{Id: "commit_known", Author: subjectID}
	imported := &corev1.Commit{Id: "commit_unknown", Author: "user_ghost"}
	authorless := &corev1.Commit{Id: "commit_none"}
	if err := handlers.Repository.resolveCommitAuthors(ctx, authored, imported, authorless); err != nil {
		t.Fatal(err)
	}
	if authored.Author != "riley" {
		t.Fatalf("authored commit author = %q, want riley", authored.Author)
	}
	if imported.Author != "user_ghost" {
		t.Fatalf("unresolved commit author = %q, want user_ghost", imported.Author)
	}
	if authorless.Author != "" {
		t.Fatalf("authorless commit author = %q, want empty", authorless.Author)
	}
}

func TestUpdateSliceDefinitionRejectsHomeIncludedPathChange(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), "clerk_nic", "nic@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	if _, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "nic"}); err != nil {
		t.Fatal(err)
	}
	home, err := handlers.Slice.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "nic", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Changing the home slice's included paths must be rejected.
	_, err = handlers.Slice.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                home.Id,
		ExpectedDefinitionHash: home.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/nic", "/nic/extra"},
			Visibility:    home.Definition.Visibility,
		},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("changing home included paths: got err=%v, want InvalidArgument", err)
	}

	// Other fields (visibility) may still change while paths are unchanged.
	updated, err := handlers.Slice.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                home.Id,
		ExpectedDefinitionHash: home.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: home.Definition.IncludedPaths,
			Visibility:    "private",
		},
	})
	if err != nil {
		t.Fatalf("changing home visibility (paths unchanged): %v", err)
	}
	if updated.Visibility != "private" {
		t.Fatalf("home visibility = %q, want private", updated.Visibility)
	}
}

func TestSimpleServiceMethodsUseInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()

	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	authStatus, err := handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if authStatus.SubjectId != "user_alice" {
		t.Fatalf("auth subject = %q, want user_alice", authStatus.SubjectId)
	}

	mem.AddAccountRole("user_alice", "alice", "admin")
	authStatus, err = handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(authStatus.Accounts) < 2 || authStatus.Accounts[0] != "alice" {
		t.Fatalf("auth accounts = %#v, want personal account first", authStatus.Accounts)
	}

	data := []byte("read me\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_readme", []storage.FileEntry{{
		Path:        "/acme/readme.txt",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/readme.txt"})

	resolved, err := handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: "commit_readme", Path: "/acme/readme.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		t.Fatalf("unexpected resolved entry: %#v", resolved.Entry)
	}
	commit, err := handlers.Repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: "commit_readme"})
	if err != nil {
		t.Fatal(err)
	}
	if commit.Id != "commit_readme" {
		t.Fatalf("commit id = %q", commit.Id)
	}
	hydrated, err := handlers.Workspace.HydratePaths(ctx, &corev1.HydratePathsRequest{Paths: []string{"/acme/readme.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(hydrated.Data) != "read me\n" {
		t.Fatalf("hydrated data = %q", string(hydrated.Data))
	}
	validated, err := handlers.Workspace.ValidateWorkspaceDiff(ctx, &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    &corev1.WorkspaceRef{Id: "acme/home"},
		BaseCommitId: "commit_readme",
		FileEdits:    []*corev1.FileEdit{{Op: "delete", Path: "/acme/readme.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.PathBases) != 1 || !validated.PathBases[0].Exists {
		t.Fatalf("unexpected validation path bases: %#v", validated.PathBases)
	}
	recorded, err := handlers.Workspace.RecordWorkspaceOperation(ctx, &corev1.RecordWorkspaceOperationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if recorded.OperationId == "" {
		t.Fatalf("empty operation id")
	}
}

func TestPublicSliceReadsAllowAnonymousContext(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := context.Background()

	publicData := []byte("public readme\n")
	publicHash := objectid.RawContentHash(publicData)
	privateData := []byte("private secret\n")
	privateHash := objectid.RawContentHash(privateData)
	mem.PutObject(filesystem.BlobKey(publicHash), publicData)
	mem.PutObject(filesystem.BlobKey(privateHash), privateData)
	mem.PutCommitWithFiles("commit_public_slice", []storage.FileEntry{
		{
			Path:        "/acme/public/readme.txt",
			BlobID:      objectid.BlobID(publicData),
			ContentHash: publicHash,
			Mode:        0o100644,
			Size:        int64(len(publicData)),
		},
		{
			Path:        "/acme/private/secret.txt",
			BlobID:      objectid.BlobID(privateData),
			ContentHash: privateHash,
			Mode:        0o100644,
			Size:        int64(len(privateData)),
		},
	}, []string{"/acme/public/readme.txt", "/acme/private/secret.txt"})
	publicSlice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "public"}, []string{"/acme/public"}, "public")
	privateSlice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "private"}, []string{"/acme/private"}, "private")

	resolvedSlice, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: publicSlice.Id})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedSlice.Definition.GetVisibility() != "public" {
		t.Fatalf("public slice visibility = %q", resolvedSlice.Definition.GetVisibility())
	}
	if _, err := handlers.Slice.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: privateSlice.Id}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("private anonymous GetSlice error = %v, want Unauthenticated", err)
	}

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.CommitId != "commit_public_slice" {
		t.Fatalf("ref commit = %q, want commit_public_slice", ref.CommitId)
	}
	listed, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public",
		Slice:    publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "readme.txt" {
		t.Fatalf("public listed entries = %#v, want readme.txt", listed.Entries)
	}
	resolvedPath, err := handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public/readme.txt",
		Slice:    publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPath.Entry.GetKind() != corev1.EntryKind_ENTRY_KIND_FILE {
		t.Fatalf("resolved public path = %#v", resolvedPath.Entry)
	}
	read, err := handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public/readme.txt",
		Slice:    publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != string(publicData) {
		t.Fatalf("public read data = %q", string(read.Data))
	}
	history, err := handlers.Repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		Slice: publicSlice.Ref,
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Commits) != 1 || history.Commits[0].Id != "commit_public_slice" {
		t.Fatalf("public history = %#v, want commit_public_slice", history.Commits)
	}
	authorCtx := authctx.WithSubjectID(context.Background(), "user_alice")
	nextData := []byte("public readme v2\n")
	uploaded, err := handlers.Blob.UploadBlob(authorCtx, &corev1.UploadBlobRequest{
		Data:  nextData,
		Slice: publicSlice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	changeset, err := handlers.Changeset.CreateChangeset(authorCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: publicSlice.Ref,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "public changeset",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(authorCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  changeset.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/public/readme.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: uploaded.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	listedChangesets, err := handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: publicSlice.Ref,
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listedChangesets.Changesets) != 1 || listedChangesets.Changesets[0].Id != changeset.Id {
		t.Fatalf("public changesets = %#v, want %s", listedChangesets.Changesets, changeset.Id)
	}
	gotChangeset, err := handlers.Changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changeset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if gotChangeset.Id != changeset.Id {
		t.Fatalf("public GetChangeset id = %q, want %q", gotChangeset.Id, changeset.Id)
	}
	diff, err := handlers.Changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: changeset.Id,
		Patchset:    patchset.Id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-public readme") || !strings.Contains(diff.Diff, "+public readme v2") {
		t.Fatalf("public changeset diff missing expected content:\n%s", diff.Diff)
	}
	_, err = handlers.Changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: privateSlice.Ref,
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("private anonymous ListChangesets error = %v, want Unauthenticated", err)
	}

	_, err = handlers.Repository.ResolvePath(ctx, &corev1.ResolvePathRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/private/secret.txt",
		Slice:    publicSlice.Ref,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("outside public slice ResolvePath error = %v, want PermissionDenied", err)
	}
	_, err = handlers.Repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/public/readme.txt",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous unscoped ReadFile error = %v, want Unauthenticated", err)
	}
}

func TestRepositoryListDirectoryPaginationUsesCursor(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	data := []byte("page\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	files := make([]storage.FileEntry, 0, 7)
	for i := range 7 {
		files = append(files, storage.FileEntry{
			Path:        fmt.Sprintf("/acme/page/file_%02d.txt", i),
			BlobID:      objectid.BlobID(data),
			ContentHash: hash,
			Mode:        0o100644,
			Size:        int64(len(data)),
		})
	}
	mem.PutCommitWithFiles("commit_page", files, nil)

	first, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: "commit_page",
		Path:     "/acme/page",
		PageSize: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 3 || first.NextCursor == "" {
		t.Fatalf("first page = %#v cursor=%q, want 3 entries and next cursor", first.Entries, first.NextCursor)
	}
	second, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: "commit_page",
		Path:     "/acme/page",
		PageSize: 3,
		Cursor:   first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 3 || second.NextCursor == "" {
		t.Fatalf("second page = %#v cursor=%q, want 3 entries and next cursor", second.Entries, second.NextCursor)
	}
	third, err := handlers.Repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: "commit_page",
		Path:     "/acme/page",
		PageSize: 3,
		Cursor:   second.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Entries) != 1 || third.NextCursor != "" {
		t.Fatalf("third page = %#v cursor=%q, want final single entry", third.Entries, third.NextCursor)
	}
	if first.Entries[0].Name != "file_00.txt" || third.Entries[0].Name != "file_06.txt" {
		t.Fatalf("unexpected page ordering: first=%#v third=%#v", first.Entries, third.Entries)
	}
}

func TestRepositoryReadFileRejectsNegativeRange(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	data := []byte("range\n")
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_range", []storage.FileEntry{{
		Path:        "/acme/range.txt",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, nil)

	for _, req := range []*corev1.ReadFileRequest{
		{CommitId: "commit_range", Path: "/acme/range.txt", Offset: -1},
		{CommitId: "commit_range", Path: "/acme/range.txt", Length: -1},
	} {
		_, err := handlers.Repository.ReadFile(ctx, req)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("ReadFile(%#v) error = %v, want InvalidArgument", req, err)
		}
	}
}

func TestCreateChangesetRejectsNonDefaultTargetRef(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	_, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      "refs/global/branches/feature",
		Title:          "branch attempt",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateChangeset target ref error = %v, want InvalidArgument", err)
	}
}

func TestCreateChangesetDefaultsEmptyTargetRef(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		Title:          "omitted target ref",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.TargetRef != storage.DefaultTargetRef {
		t.Fatalf("CreateChangeset target ref = %q, want %q", cs.TargetRef, storage.DefaultTargetRef)
	}
}

func TestChangesetUpdateRejectsMalformedFileEdits(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit *corev1.FileEdit
	}{
		{name: "empty delete path", edit: &corev1.FileEdit{Op: "delete"}},
		{name: "empty mkdir path", edit: &corev1.FileEdit{Op: "mkdir"}},
		{name: "rename missing old path", edit: &corev1.FileEdit{Op: "rename", Path: "/acme/new.txt"}},
		{name: "rename missing new path", edit: &corev1.FileEdit{Op: "rename", OldPath: "/acme/old.txt"}},
		{name: "rename same path", edit: &corev1.FileEdit{Op: "rename", OldPath: "/acme/same.txt", Path: "/acme/same.txt"}},
		{name: "old path on upsert", edit: &corev1.FileEdit{Op: "upsert", OldPath: "/acme/old.txt", Path: "/acme/new.txt", BlobId: "blob_missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
				AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
				TargetRef:      storage.DefaultTargetRef,
				BaseCommitId:   ref.CommitId,
				Title:          tc.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
				ChangesetId:  cs.Id,
				BaseCommitId: ref.CommitId,
				FileEdits:    []*corev1.FileEdit{tc.edit},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("UpdateChangeset error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestChangesetUpdateValidatesAndHydratesBlobContentHash(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("blob metadata\n"), Slice: &corev1.SliceRef{Account: "acme", Slice: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "blob metadata",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/wrong-hash.txt",
			BlobId:      uploaded.BlobId,
			ContentHash: "sha256:not-the-uploaded-bytes",
			Mode:        0o100644,
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UpdateChangeset wrong hash error = %v, want InvalidArgument", err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:     "upsert",
			Path:   "/acme/hydrated-hash.txt",
			BlobId: uploaded.BlobId,
			Mode:   0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := patchset.FileEdits[0].ContentHash; got != uploaded.ContentHash {
		t.Fatalf("patchset content hash = %q, want %q", got, uploaded.ContentHash)
	}
}

func TestAuthChooseUsernameForExternalSubjectInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	subjectID, err := mem.Auth.EnsureExternalSubject(context.Background(), "clerk_user_123", "Taylor.Example@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)

	authStatus, err := handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !authStatus.NeedsUsername || len(authStatus.Accounts) != 0 {
		t.Fatalf("auth status = %#v, want needs username with no accounts", authStatus)
	}

	available, err := handlers.Auth.CheckUsernameAvailable(ctx, &corev1.CheckUsernameAvailableRequest{Username: "Taylor_Name"})
	if err != nil {
		t.Fatal(err)
	}
	if !available.Available || available.Normalized != "taylor-name" || available.Reason != "" {
		t.Fatalf("availability = %#v, want available normalized taylor-name", available)
	}

	chosen, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "Taylor_Name"})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.SubjectId != subjectID || chosen.Account != "taylor-name" {
		t.Fatalf("chosen username = %#v, want subject %q account taylor-name", chosen, subjectID)
	}

	authStatus, err = handlers.Auth.GetAuthStatus(ctx, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if authStatus.NeedsUsername || len(authStatus.Accounts) == 0 || authStatus.Accounts[0] != "taylor-name" {
		t.Fatalf("auth status after choose = %#v, want taylor-name account", authStatus)
	}

	retried, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: "another-name"})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Account != "taylor-name" {
		t.Fatalf("retried choose account = %q, want existing taylor-name", retried.Account)
	}

	available, err = handlers.Auth.CheckUsernameAvailable(ctx, &corev1.CheckUsernameAvailableRequest{Username: "taylor_name"})
	if err != nil {
		t.Fatal(err)
	}
	if available.Available || available.Normalized != "taylor-name" || available.Reason != "username is taken" {
		t.Fatalf("taken availability = %#v, want taken taylor-name", available)
	}
}

func TestAuthCLILoginDeviceFlowInMemoryStorage(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := context.Background()

	start, err := handlers.Auth.StartCliLogin(ctx, &corev1.StartCliLoginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if start.Code == "" || start.ExpiresAt == "" || start.PollIntervalSeconds != 2 {
		t.Fatalf("start CLI login = %#v, want code, expiry, poll interval 2", start)
	}

	pending, err := handlers.Auth.PollCliLogin(ctx, &corev1.PollCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.Token != "" || pending.SubjectId != "" {
		t.Fatalf("pending poll = %#v, want pending without token", pending)
	}

	completeCtx := authctx.WithSubjectID(ctx, "user_alice")
	complete, err := handlers.Auth.CompleteCliLogin(completeCtx, &corev1.CompleteCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if complete.SubjectId != "user_alice" {
		t.Fatalf("complete subject = %q, want user_alice", complete.SubjectId)
	}

	approved, err := handlers.Auth.PollCliLogin(ctx, &corev1.PollCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.Token == "" || approved.SubjectId != "user_alice" {
		t.Fatalf("approved poll = %#v, want approved token for user_alice", approved)
	}
	subject, err := mem.Auth.SubjectForToken(ctx, approved.Token)
	if err != nil {
		t.Fatal(err)
	}
	if subject.ID != "user_alice" {
		t.Fatalf("session subject = %q, want user_alice", subject.ID)
	}

	second, err := handlers.Auth.PollCliLogin(ctx, &corev1.PollCliLoginRequest{Code: start.Code})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "expired" || second.Token != "" || second.SubjectId != "" {
		t.Fatalf("second poll = %#v, want expired without token", second)
	}
}

func newMemoryHandlers() (*memory.Stores, *Handlers) {
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	handlers := New(Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects)
	return mem, handlers
}

func createStandaloneChangesetWithPatchset(t *testing.T, ctx context.Context, handlers *Handlers, baseCommitID, title, p, content string) (*corev1.Changeset, *corev1.Patchset) {
	t.Helper()
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   baseCommitID,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte(content),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        p,
			BlobId:      blob.BlobId,
			ContentHash: blob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs, patchset
}

func createStackEntryWithPatchset(t *testing.T, ctx context.Context, handlers *Handlers, stackID, parentChangesetID, parentPatchsetID, baseCommitID, title, p, content string) (*corev1.Changeset, *corev1.Patchset) {
	t.Helper()
	entry, err := handlers.Stack.AddStackEntry(ctx, &corev1.AddStackEntryRequest{
		StackId:           stackID,
		Title:             title,
		ParentChangesetId: parentChangesetID,
		ParentPatchsetId:  parentPatchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte(content),
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &corev1.UpdateChangesetRequest{
		ChangesetId:  entry.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        p,
			BlobId:      blob.BlobId,
			ContentHash: blob.ContentHash,
			Mode:        0o100644,
		}},
	}
	if parentPatchsetID != "" {
		req.BaseKind = "patchset"
		req.BasePatchsetId = parentPatchsetID
		req.ExpectedParentPatchsetId = parentPatchsetID
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return entry, patchset
}

func stackEntryForTest(t *testing.T, stack *corev1.ChangesetStack, changesetID string) *corev1.ChangesetStackEntry {
	t.Helper()
	for _, entry := range stack.Entries {
		if entry.ChangesetId == changesetID {
			return entry
		}
	}
	t.Fatalf("stack entry %s not found in %#v", changesetID, stack.Entries)
	return nil
}

func stackContainsEntry(stack *corev1.ChangesetStack, changesetID string) bool {
	for _, entry := range stack.Entries {
		if entry != nil && entry.ChangesetId == changesetID {
			return true
		}
	}
	return false
}

func sliceListContains(slices []*corev1.Slice, slug string) bool {
	for _, slice := range slices {
		if slice.Ref != nil && slice.Ref.Slice == slug {
			return true
		}
	}
	return false
}
