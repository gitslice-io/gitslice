package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
