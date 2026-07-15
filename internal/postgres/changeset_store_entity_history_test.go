package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestEntityHistoryBatchedMixedPatchsetMatchesRowSemantics(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	prefix := "/acme/payment/entity_batch"
	addPath := prefix + "/added.txt"
	modifyPath := prefix + "/modified.txt"
	renameOldPath := prefix + "/rename_old.txt"
	renameNewPath := prefix + "/rename_new.txt"
	deletePath := prefix + "/deleted.txt"
	dirPath := prefix + "/created_dir"
	exactOldPath := prefix + "/exact_old.txt"
	exactNewPath := prefix + "/exact_new.txt"

	modifyOldID, modifyOldHash := upsertTestBlob(t, ctx, store, "modify old\n")
	renameID, renameHash := upsertTestBlob(t, ctx, store, "rename me\n")
	deleteID, deleteHash := upsertTestBlob(t, ctx, store, "delete me\n")
	exactID, exactHash := upsertTestBlob(t, ctx, store, "same exact content\n")
	seed := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "upsert", Path: modifyPath, BlobId: modifyOldID, ContentHash: modifyOldHash, Mode: 0o100644},
		{Op: "upsert", Path: renameOldPath, BlobId: renameID, ContentHash: renameHash, Mode: 0o100755},
		{Op: "upsert", Path: deletePath, BlobId: deleteID, ContentHash: deleteHash, Mode: 0o100600},
		{Op: "upsert", Path: exactOldPath, BlobId: exactID, ContentHash: exactHash, Mode: 0o100644},
	})
	publishAndDrainEntityHistoryPatchset(t, ctx, store, seed)
	seedRows := currentEntityRowsForTest(t, ctx, store, prefix)

	addID, addHash := upsertTestBlob(t, ctx, store, "added\n")
	modifyNewID, modifyNewHash := upsertTestBlob(t, ctx, store, "modify new\n")
	base = getTestRef(t, ctx, store)
	patchset := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "upsert", Path: addPath, BlobId: addID, ContentHash: addHash, Mode: 0o100644},
		{Op: "upsert", Path: modifyPath, BlobId: modifyNewID, ContentHash: modifyNewHash, Mode: 0o100755},
		{Op: "rename", OldPath: renameOldPath, Path: renameNewPath},
		{Op: "delete", Path: deletePath},
		{Op: "mkdir", Path: dirPath},
		{Op: "delete", Path: exactOldPath},
		{Op: "upsert", Path: exactNewPath, BlobId: exactID, ContentHash: exactHash, Mode: 0o100644},
	})
	commitID := publishAndDrainEntityHistoryPatchset(t, ctx, store, patchset)

	current := currentEntityRowsForTest(t, ctx, store, prefix)
	assertCurrentEntityRows(t, current, map[string]entityHistoryCurrentWant{
		addPath:       {Kind: "file", ContentHash: addHash, Mode: 0o100644},
		modifyPath:    {EntityID: seedRows[modifyPath].EntityID, Kind: "file", ContentHash: modifyNewHash, Mode: 0o100755},
		renameNewPath: {EntityID: seedRows[renameOldPath].EntityID, Kind: "file", ContentHash: renameHash, Mode: 0o100755},
		dirPath:       {Kind: "directory"},
		exactNewPath:  {EntityID: seedRows[exactOldPath].EntityID, Kind: "file", ContentHash: exactHash, Mode: 0o100644},
	})

	changes := entityChangeRowsForTest(t, ctx, store, commitID, prefix)
	assertEntityChangeRows(t, changes, []entityHistoryChangeRow{
		{Path: addPath, EntityID: current[addPath].EntityID, Kind: "file", ChangeKind: "added", Source: "explicit", Confidence: 100, ContentHash: addHash, Mode: 0o100644},
		{Path: modifyPath, EntityID: seedRows[modifyPath].EntityID, Kind: "file", ChangeKind: "modified", Source: "explicit", Confidence: 100, ContentHash: modifyNewHash, Mode: 0o100755},
		{Path: renameNewPath, EntityID: seedRows[renameOldPath].EntityID, Kind: "file", ChangeKind: "moved", OldPath: renameOldPath, Source: "explicit", Confidence: 100, ContentHash: renameHash, Mode: 0o100755},
		{Path: deletePath, EntityID: seedRows[deletePath].EntityID, Kind: "file", ChangeKind: "deleted", Source: "explicit", Confidence: 100, ContentHash: deleteHash, Mode: 0o100600},
		{Path: dirPath, EntityID: current[dirPath].EntityID, Kind: "directory", ChangeKind: "added", Source: "explicit", Confidence: 100},
		{Path: exactNewPath, EntityID: seedRows[exactOldPath].EntityID, Kind: "file", ChangeKind: "moved", OldPath: exactOldPath, Source: "exact_content_match", Confidence: 100, ContentHash: exactHash, Mode: 0o100644},
	})
}

func TestEntityHistoryBatchedAddThenRenameSamePatchset(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	prefix := "/acme/payment/entity_add_rename"
	oldPath := prefix + "/created.txt"
	newPath := prefix + "/renamed.txt"
	blobID, contentHash := upsertTestBlob(t, ctx, store, "created then renamed\n")
	patchset := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "upsert", Path: oldPath, BlobId: blobID, ContentHash: contentHash, Mode: 0o100644},
		{Op: "rename", OldPath: oldPath, Path: newPath},
	})
	commitID := publishAndDrainEntityHistoryPatchset(t, ctx, store, patchset)

	current := currentEntityRowsForTest(t, ctx, store, prefix)
	assertCurrentEntityRows(t, current, map[string]entityHistoryCurrentWant{
		newPath: {Kind: "file", ContentHash: contentHash, Mode: 0o100644},
	})
	entityID := current[newPath].EntityID
	changes := entityChangeRowsForTest(t, ctx, store, commitID, prefix)
	assertEntityChangeRows(t, changes, []entityHistoryChangeRow{
		{Path: oldPath, EntityID: entityID, Kind: "file", ChangeKind: "added", Source: "explicit", Confidence: 100, ContentHash: contentHash, Mode: 0o100644},
		{Path: newPath, EntityID: entityID, Kind: "file", ChangeKind: "moved", OldPath: oldPath, Source: "explicit", Confidence: 100, ContentHash: contentHash, Mode: 0o100644},
	})
}

func publishAndDrainEntityHistoryPatchset(t *testing.T, ctx context.Context, store *DB, patchset *corev1.Patchset) string {
	t.Helper()
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	commitID := getTestRef(t, ctx, store).CommitId
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.Changesets().WaitForOutboxDrain(drainCtx); err != nil {
		t.Fatal(err)
	}
	return commitID
}

type entityHistoryCurrentRow struct {
	Path        string
	AccountID   string
	EntityID    string
	Kind        string
	ContentHash string
	Mode        uint32
}

type entityHistoryCurrentWant struct {
	EntityID    string
	Kind        string
	ContentHash string
	Mode        uint32
}

func currentEntityRowsForTest(t *testing.T, ctx context.Context, store *DB, prefix string) map[string]entityHistoryCurrentRow {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `
		select path, account_id, entity_id, kind, coalesce(content_hash, ''), coalesce(mode, 0)
		from current_path_entities
		where target_ref = $1
		  and (path = $2 or path like $3)
		order by path
	`, DefaultTargetRef, prefix, prefix+"/%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]entityHistoryCurrentRow{}
	for rows.Next() {
		var row entityHistoryCurrentRow
		var mode int
		if err := rows.Scan(&row.Path, &row.AccountID, &row.EntityID, &row.Kind, &row.ContentHash, &mode); err != nil {
			t.Fatal(err)
		}
		if mode > 0 {
			row.Mode = uint32(mode)
		}
		out[row.Path] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertCurrentEntityRows(t *testing.T, got map[string]entityHistoryCurrentRow, want map[string]entityHistoryCurrentWant) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("current rows = %#v, want %#v", got, want)
	}
	for p, wantRow := range want {
		gotRow, ok := got[p]
		if !ok {
			t.Fatalf("missing current row %s in %#v", p, got)
		}
		if gotRow.AccountID != "acct_acme" {
			t.Fatalf("current row %s account = %q, want acct_acme", p, gotRow.AccountID)
		}
		if wantRow.EntityID != "" && gotRow.EntityID != wantRow.EntityID {
			t.Fatalf("current row %s entity = %q, want %q", p, gotRow.EntityID, wantRow.EntityID)
		}
		if gotRow.EntityID == "" {
			t.Fatalf("current row %s has empty entity id", p)
		}
		if gotRow.Kind != wantRow.Kind || gotRow.ContentHash != wantRow.ContentHash || gotRow.Mode != wantRow.Mode {
			t.Fatalf("current row %s = %#v, want kind=%s hash=%s mode=%#o", p, gotRow, wantRow.Kind, wantRow.ContentHash, wantRow.Mode)
		}
	}
}

type entityHistoryChangeRow struct {
	Path        string
	EntityID    string
	Kind        string
	ChangeKind  string
	OldPath     string
	Source      string
	Confidence  int
	ContentHash string
	Mode        uint32
}

func entityChangeRowsForTest(t *testing.T, ctx context.Context, store *DB, commitID, prefix string) []entityHistoryChangeRow {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `
		select path, entity_id, kind, change_kind, coalesce(old_path, ''),
		       source, confidence, coalesce(content_hash, ''), coalesce(mode, 0)
		from commit_entity_changes
		where target_ref = $1
		  and commit_id = $2
		  and (path = $3 or path like $4)
		order by path, change_kind, old_path, entity_id
	`, DefaultTargetRef, commitID, prefix, prefix+"/%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []entityHistoryChangeRow
	for rows.Next() {
		var row entityHistoryChangeRow
		var mode int
		if err := rows.Scan(&row.Path, &row.EntityID, &row.Kind, &row.ChangeKind, &row.OldPath, &row.Source, &row.Confidence, &row.ContentHash, &mode); err != nil {
			t.Fatal(err)
		}
		if mode > 0 {
			row.Mode = uint32(mode)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertEntityChangeRows(t *testing.T, got, want []entityHistoryChangeRow) {
	t.Helper()
	sortEntityChangeRows(got)
	sortEntityChangeRows(want)
	if len(got) != len(want) {
		t.Fatalf("entity change rows = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entity change row[%d] = %#v, want %#v; all rows %#v", i, got[i], want[i], got)
		}
	}
}

func sortEntityChangeRows(rows []entityHistoryChangeRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		if rows[i].ChangeKind != rows[j].ChangeKind {
			return rows[i].ChangeKind < rows[j].ChangeKind
		}
		if rows[i].OldPath != rows[j].OldPath {
			return rows[i].OldPath < rows[j].OldPath
		}
		return rows[i].EntityID < rows[j].EntityID
	})
}
