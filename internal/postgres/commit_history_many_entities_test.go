package postgres

import (
	"fmt"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
)

// A slice made of thousands of files (an imported project) asks for the
// history of every file's entity at once. That used to be one OR term per
// entity: with 5,500 files the planner ran Neon out of memory (SQLSTATE
// 53200) after 12 seconds, and the slice's history page returned a 500.
func TestHistoryOfThousandsOfEntitiesStaysCheap(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	prefix := "/acme/payment/many"
	blobID, hash := upsertTestBlob(t, ctx, store, "content\n")
	patchset := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "upsert", Path: prefix + "/a.txt", BlobId: blobID, ContentHash: hash, Mode: 0o100644},
		{Op: "upsert", Path: prefix + "/b.txt", BlobId: blobID, ContentHash: hash, Mode: 0o100644},
	})
	commitID := publishAndDrainEntityHistoryPatchset(t, ctx, store, patchset)
	rows := currentEntityRowsForTest(t, ctx, store, prefix)
	real := HistoryEntityRef{AccountID: rows[prefix+"/a.txt"].AccountID, EntityID: rows[prefix+"/a.txt"].EntityID}
	if real.EntityID == "" {
		t.Fatalf("no entity for a.txt: %+v", rows)
	}

	// 40,000 entities: the real one, among thousands nothing ever changed.
	// That is past what the old OR chain could even send: it bound two
	// parameters per entity, and Postgres allows 65,535 per statement.
	refs := make([]HistoryEntityRef, 0, 40000)
	for i := 0; i < 39999; i++ {
		refs = append(refs, HistoryEntityRef{AccountID: real.AccountID, EntityID: fmt.Sprintf("ent_missing_%05d", i)})
	}
	refs = append(refs, real)

	within := func(name string, run func() ([]string, error)) {
		t.Helper()
		start := time.Now()
		ids, err := run()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("%s took %s", name, took)
		}
		for _, id := range ids {
			if id == commitID {
				return
			}
		}
		t.Fatalf("%s = %v, want the commit %s that changed the entity", name, ids, commitID)
	}
	ids := func(page *CommitListPage, err error) ([]string, error) {
		if err != nil {
			return nil, err
		}
		var out []string
		for _, c := range page.Commits {
			out = append(out, c.Id)
		}
		return out, nil
	}
	within("by entities", func() ([]string, error) {
		return ids(store.Repository().ListCommitPageByEntityRefs(ctx, DefaultTargetRef, refs, 10, ""))
	})
	within("by entities or path prefixes", func() ([]string, error) {
		return ids(store.Repository().ListCommitPageByEntityRefsOrPathPrefixes(ctx, DefaultTargetRef, refs, []string{prefix}, 10, ""))
	})
	within("resolving a commit prefix", func() ([]string, error) {
		commits, err := store.Repository().ResolveCommitCandidates(ctx, storage.CommitResolveFilter{
			RefName: DefaultTargetRef, IDPrefix: commitID[:20], EntityRefs: refs, Limit: 4,
		})
		if err != nil {
			return nil, err
		}
		var out []string
		for _, c := range commits {
			out = append(out, c.Id)
		}
		return out, nil
	})
}
