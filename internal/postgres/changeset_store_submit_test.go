package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestSubmitBatchesMixedPathHeads(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	updatePath := "/acme/payment/batch_update.txt"
	deletePath := "/acme/payment/batch_delete.txt"
	renameOldPath := "/acme/payment/batch_rename_old.txt"
	renameNewPath := "/acme/payment/batch_rename_new.txt"
	directoryPath := "/acme/payment/batch_directory"

	updateOldID, updateOldHash := upsertTestBlob(t, ctx, store, "update before batch\n")
	deleteID, deleteHash := upsertTestBlob(t, ctx, store, "delete in batch\n")
	renameID, renameHash := upsertTestBlob(t, ctx, store, "rename in batch\n")
	seed := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "upsert", Path: updatePath, BlobId: updateOldID, ContentHash: updateOldHash, Mode: 0o100644},
		{Op: "upsert", Path: deletePath, BlobId: deleteID, ContentHash: deleteHash, Mode: 0o100644},
		{Op: "upsert", Path: renameOldPath, BlobId: renameID, ContentHash: renameHash, Mode: 0o100755},
	})
	if _, err := store.Changesets().Submit(ctx, seed.ChangesetId, seed.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("seed published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	addedID, addedHash := upsertTestBlob(t, ctx, store, "added in batch\n")
	updateNewID, updateNewHash := upsertTestBlob(t, ctx, store, "update after batch\n")
	edits := make([]*corev1.FileEdit, 0, 50)
	addedPaths := make([]string, 0, 46)
	for i := 0; i < 46; i++ {
		p := fmt.Sprintf("/acme/payment/batch_add_%02d.txt", i)
		addedPaths = append(addedPaths, p)
		edits = append(edits, &corev1.FileEdit{Op: "upsert", Path: p, BlobId: addedID, ContentHash: addedHash, Mode: 0o100644})
	}
	edits = append(edits,
		&corev1.FileEdit{Op: "upsert", Path: updatePath, BlobId: updateNewID, ContentHash: updateNewHash, Mode: 0o100600},
		&corev1.FileEdit{Op: "delete", Path: deletePath},
		&corev1.FileEdit{Op: "rename", OldPath: renameOldPath, Path: renameNewPath},
		&corev1.FileEdit{Op: "mkdir", Path: directoryPath},
	)
	patchset := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, edits)
	if len(patchset.PathBases) != 51 {
		t.Fatalf("path bases = %d, want 51", len(patchset.PathBases))
	}
	submit, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id)
	if err != nil {
		t.Fatal(err)
	}
	if submit.Status != "pending_publish" {
		t.Fatalf("submit status = %q, want pending_publish", submit.Status)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("batch published = %d, want 1", published)
	}

	addedWant := pathHeadFromFile(FileEntry{
		BlobID:      addedID,
		ContentHash: addedHash,
		Mode:        0o100644,
		Size:        int64(len("added in batch\n")),
	})
	for _, p := range addedPaths {
		want := addedWant
		want.Path = p
		assertSubmitPathHead(t, pathHeadForSubmitTest(t, ctx, store, p), want)
	}
	assertSubmitPathHead(t, pathHeadForSubmitTest(t, ctx, store, updatePath), pathHeadFromFile(FileEntry{
		Path:        updatePath,
		BlobID:      updateNewID,
		ContentHash: updateNewHash,
		Mode:        0o100600,
		Size:        int64(len("update after batch\n")),
	}))
	assertSubmitPathHead(t, pathHeadForSubmitTest(t, ctx, store, deletePath), PathHead{
		Path:             deletePath,
		EntryFingerprint: MissingEntryFingerprint(),
	})
	assertSubmitPathHead(t, pathHeadForSubmitTest(t, ctx, store, renameOldPath), PathHead{
		Path:             renameOldPath,
		EntryFingerprint: MissingEntryFingerprint(),
	})
	assertSubmitPathHead(t, pathHeadForSubmitTest(t, ctx, store, renameNewPath), pathHeadFromFile(FileEntry{
		Path:        renameNewPath,
		BlobID:      renameID,
		ContentHash: renameHash,
		Mode:        0o100755,
		Size:        int64(len("rename in batch\n")),
	}))
	assertSubmitPathHead(t, pathHeadForSubmitTest(t, ctx, store, directoryPath), PathHead{
		Path:             directoryPath,
		Exists:           true,
		EntryFingerprint: DirectoryEntryFingerprint(treestore.EmptyRootID()),
	})
}

func TestSubmitBatchedPathBaseFingerprintMismatch(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	p := "/acme/payment/batch_fingerprint.txt"
	oldID, oldHash := upsertTestBlob(t, ctx, store, "fingerprint before\n")
	seed := createDraftPatchset(t, ctx, store, base.CommitId, p, oldID, oldHash)
	if _, err := store.Changesets().Submit(ctx, seed.ChangesetId, seed.Id); err != nil {
		t.Fatal(err)
	}
	if published, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}

	base = getTestRef(t, ctx, store)
	newID, newHash := upsertTestBlob(t, ctx, store, "fingerprint after\n")
	patchset := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{{
		Op:          "upsert",
		Path:        p,
		BlobId:      newID,
		ContentHash: newHash,
		Mode:        0o100644,
	}})
	if _, err := store.db.ExecContext(ctx, `
		update path_heads
		set entry_fingerprint = 'different-fingerprint'
		where path = $1
	`, p); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); !errors.Is(err, ErrConflict) {
		t.Fatalf("Submit err = %v, want ErrConflict", err)
	}
	var reason string
	if err := store.db.QueryRowContext(ctx, `
		select submit_blocked_reason
		from changesets
		where id = $1
	`, patchset.ChangesetId).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "path base conflict, refresh or rebase the changeset" {
		t.Fatalf("submit_blocked_reason = %q", reason)
	}
}

func TestSubmitBatchedDuplicateEditsLaterWins(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	p := "/acme/payment/batch_duplicate.txt"
	firstID, firstHash := upsertTestBlob(t, ctx, store, "duplicate first\n")
	lastID, lastHash := upsertTestBlob(t, ctx, store, "duplicate last\n")
	patchset := createDraftPatchsetWithEdits(t, ctx, store, base.CommitId, []*corev1.FileEdit{
		{Op: "upsert", Path: p, BlobId: firstID, ContentHash: firstHash, Mode: 0o100644},
		{Op: "upsert", Path: p, BlobId: lastID, ContentHash: lastHash, Mode: 0o100755},
	})
	if _, err := store.Changesets().Submit(ctx, patchset.ChangesetId, patchset.Id); err != nil {
		t.Fatal(err)
	}
	assertSubmitPathHead(t, pathHeadForSubmitTest(t, ctx, store, p), pathHeadFromFile(FileEntry{
		Path:        p,
		BlobID:      lastID,
		ContentHash: lastHash,
		Mode:        0o100755,
		Size:        int64(len("duplicate last\n")),
	}))
}

func TestBatchedPathHeadEditErrors(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(context.Context, *sql.Tx) error
		edits   []*corev1.FileEdit
		wantErr error
	}{
		{
			name: "rename missing path",
			prepare: func(ctx context.Context, tx *sql.Tx) error {
				return upsertPathHeadsTx(ctx, tx, []PathHead{{
					Path:             "/acme/payment/missing_rename.txt",
					EntryFingerprint: MissingEntryFingerprint(),
				}}, "", "")
			},
			edits: []*corev1.FileEdit{{
				Op:      "rename",
				OldPath: "/acme/payment/missing_rename.txt",
				Path:    "/acme/payment/missing_rename_new.txt",
			}},
			wantErr: ErrConflict,
		},
		{
			name:    "missing blob",
			prepare: func(context.Context, *sql.Tx) error { return nil },
			edits: []*corev1.FileEdit{{
				Op:     "upsert",
				Path:   "/acme/payment/missing_blob.txt",
				BlobId: "blob_missing",
				Mode:   0o100644,
			}},
			wantErr: ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, store := newPostgresTestStore(t)
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := tt.prepare(ctx, tx); err != nil {
				t.Fatal(err)
			}
			if err := applyPathHeadEditsTx(ctx, tx, "", "", tt.edits); !errors.Is(err, tt.wantErr) {
				t.Fatalf("applyPathHeadEditsTx err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestBatchedTreeEditsMissingBlob(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = treeEditsFromPatchsetTx(ctx, tx, []*corev1.FileEdit{{
		Op:     "upsert",
		Path:   "/acme/payment/missing_tree_blob.txt",
		BlobId: "blob_missing",
		Mode:   0o100644,
	}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("treeEditsFromPatchsetTx err = %v, want ErrNotFound", err)
	}
}

func pathHeadForSubmitTest(t *testing.T, ctx context.Context, store *DB, p string) PathHead {
	t.Helper()
	head, err := scanPathHead(store.db.QueryRowContext(ctx, `
		select path, exists, entry_fingerprint, blob_id, content_hash, mode, size
		from path_heads
		where path = $1
	`, p))
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func assertSubmitPathHead(t *testing.T, got, want PathHead) {
	t.Helper()
	if got != want {
		t.Fatalf("path head = %#v, want %#v", got, want)
	}
}
