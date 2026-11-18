package events

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type noopEmitter struct{}

func (noopEmitter) Emit(context.Context, storage.Event) {}
func (noopEmitter) Flush(context.Context)               {}

func TestWrapChangesetsForwardsSubmitChangesetAndRecordBlockedReason(t *testing.T) {
	mem := memory.New()
	wrapped := WrapChangesets(mem.Changesets, noopEmitter{})

	ref := &corev1.SliceRef{Account: "acme", Slice: "guarded"}
	mem.PutSliceWithSubmitSettings(ref, []string{"/acme/guarded"}, "private", 1, []string{"ci"})

	ctx := context.Background()
	headRef, err := mem.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	cs, err := wrapped.Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: ref,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   headRef.GetCommitId(),
		Title:          "test b3",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ps, err := wrapped.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: headRef.GetCommitId(),
		Author:       "user_alice",
		ChangedPaths: []string{"/acme/guarded/app.go"},
		FileEdits:    []*corev1.FileEdit{{Op: "add", Path: "/acme/guarded/app.go", BlobId: "blob_1", ContentHash: "hash_1", Mode: 0o100644}},
	})
	if err != nil {
		t.Fatalf("AddPatchset: %v", err)
	}

	if err := wrapped.RecordSubmitBlockedReason(ctx, cs.Id, ps.Id, "path owner approvals not satisfied"); err != nil {
		t.Fatalf("RecordSubmitBlockedReason: %v", err)
	}
	got, err := wrapped.Get(ctx, cs.Id)
	if err != nil || got.GetSubmitBlockedReason() != "path owner approvals not satisfied" {
		t.Fatalf("Get SubmitBlockedReason = (%q, %v)", got.GetSubmitBlockedReason(), err)
	}

	// BypassRequirements submits cleanly through the event wrapper with zero runtime type assertions.
	res, err := wrapped.SubmitChangeset(ctx, storage.SubmitInput{
		ChangesetID:               cs.Id,
		ExpectedCurrentPatchsetID: ps.Id,
		BypassRequirements:        true,
	})
	if err != nil || res.GetStatus() != "pending_publish" {
		t.Fatalf("SubmitChangeset(BypassRequirements) = (%+v, %v)", res, err)
	}
}
