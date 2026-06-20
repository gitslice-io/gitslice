package postgres

import "testing"

func TestReportUnreachableConservativeRoots(t *testing.T) {
	ctx, store, objectStore := newPostgresTestStoreWithObjects(t)
	base := getTestRef(t, ctx, store)

	liveBlobID, liveContentHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst GCLive = true\n")
	createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/gc_live.go", liveBlobID, liveContentHash)

	orphanBlobID, _ := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst GCOrphan = true\n")

	abandonedBlobID, abandonedContentHash := upsertTestBlobObject(t, ctx, store, objectStore, "package payment\nconst GCAbandoned = true\n")
	abandonedPatchset := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/gc_abandoned.go", abandonedBlobID, abandonedContentHash)
	if err := store.Changesets().Abandon(ctx, abandonedPatchset.ChangesetId); err != nil {
		t.Fatal(err)
	}

	report, err := store.ReportUnreachable(ctx, objectStore, GCOptions{SampleLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.Roots.Refs == 0 {
		t.Fatalf("expected ref roots, got %#v", report.Roots)
	}
	if report.Roots.LiveChangesets != 1 {
		t.Fatalf("live changeset roots = %d, want 1", report.Roots.LiveChangesets)
	}
	if report.Roots.PendingPublish != 0 {
		t.Fatalf("pending publish roots = %d, want 0", report.Roots.PendingPublish)
	}
	if report.OrphanBlobCount != 2 {
		t.Fatalf("orphan blob count = %d, want 2; samples %#v", report.OrphanBlobCount, report.OrphanBlobs)
	}
	if len(report.OrphanBlobs) != 1 {
		t.Fatalf("orphan blob samples = %d, want sample cap 1: %#v", len(report.OrphanBlobs), report.OrphanBlobs)
	}
	if gcTestContains(report.OrphanBlobs, liveBlobID) {
		t.Fatalf("live draft blob %s was reported orphan: %#v", liveBlobID, report.OrphanBlobs)
	}
	if report.AbandonedPatchsetCount != 1 {
		t.Fatalf("abandoned patchset count = %d, want 1", report.AbandonedPatchsetCount)
	}
	if len(report.AbandonedPatchsets) != 1 || report.AbandonedPatchsets[0] != abandonedPatchset.Id {
		t.Fatalf("abandoned patchset samples = %#v, want %s", report.AbandonedPatchsets, abandonedPatchset.Id)
	}
	if !report.TreeNodeEnumerationLimited || len(report.Notes) == 0 {
		t.Fatalf("expected tree-node enumeration limitation note, got %#v", report)
	}
	if !gcTestContains(report.OrphanBlobs, orphanBlobID) && !gcTestContains(report.OrphanBlobs, abandonedBlobID) {
		t.Fatalf("orphan sample should be one of the unreachable blobs, got %#v", report.OrphanBlobs)
	}
}

func gcTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
