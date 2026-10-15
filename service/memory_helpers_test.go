package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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
	}, mem.Objects, nil)
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
