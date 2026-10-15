package postgres

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func createDraftPatchset(t *testing.T, ctx context.Context, store *DB, baseCommitID, path, blobID, contentHash string) *corev1.Patchset {
	t.Helper()
	cs, err := store.Changesets().Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   baseCommitID,
		Title:          "storage test",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := store.Changesets().AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: baseCommitID,
		Author:       "user_alice",
		ChangedPaths: []string{path},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        path,
			BlobId:      blobID,
			ContentHash: contentHash,
			Mode:        0o100644,
		}},
		PathBases: []*corev1.PathBase{{
			Path:             path,
			BaseCommitId:     baseCommitID,
			Exists:           false,
			EntryFingerprint: MissingEntryFingerprint(),
			Check:            "entry_fingerprint",
		}},
		ReadSet:  []*corev1.PathSetEntry{{Path: path}},
		WriteSet: []*corev1.PathSetEntry{{Path: path}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return patchset
}

func createDraftPatchsetWithEdits(t *testing.T, ctx context.Context, store *DB, baseCommitID string, edits []*corev1.FileEdit) *corev1.Patchset {
	t.Helper()
	cs, err := store.Changesets().Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   baseCommitID,
		Title:          "storage test",
	})
	if err != nil {
		t.Fatal(err)
	}
	changedPaths := changedPathsForTestEdits(edits)
	pathBases := make([]*corev1.PathBase, 0, len(changedPaths))
	readSet := make([]*corev1.PathSetEntry, 0, len(changedPaths))
	writeSet := make([]*corev1.PathSetEntry, 0, len(changedPaths))
	for _, p := range changedPaths {
		pathBases = append(pathBases, pathBaseForTestPath(t, ctx, store, baseCommitID, p))
		readSet = append(readSet, &corev1.PathSetEntry{Path: p})
		writeSet = append(writeSet, &corev1.PathSetEntry{Path: p})
	}
	patchset, err := store.Changesets().AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: baseCommitID,
		Author:       "user_alice",
		ChangedPaths: changedPaths,
		FileEdits:    edits,
		PathBases:    pathBases,
		ReadSet:      readSet,
		WriteSet:     writeSet,
	})
	if err != nil {
		t.Fatal(err)
	}
	return patchset
}

func changedPathsForTestEdits(edits []*corev1.FileEdit) []string {
	seen := map[string]struct{}{}
	for _, edit := range edits {
		if edit.Path != "" {
			seen[edit.Path] = struct{}{}
		}
		if edit.OldPath != "" {
			seen[edit.OldPath] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func pathBaseForTestPath(t *testing.T, ctx context.Context, store *DB, baseCommitID, p string) *corev1.PathBase {
	t.Helper()
	base := &corev1.PathBase{
		Path:             p,
		BaseCommitId:     baseCommitID,
		EntryFingerprint: MissingEntryFingerprint(),
		Check:            "entry_fingerprint",
	}
	entry, err := store.Repository().GetEntry(ctx, baseCommitID, p)
	if errors.Is(err, ErrNotFound) {
		return base
	}
	if err != nil {
		t.Fatal(err)
	}
	base.Exists = true
	base.EntryKind = entry.Kind
	switch entry.Kind {
	case "directory":
		base.TreeId = entry.TreeID
		base.EntryFingerprint = DirectoryEntryFingerprint(entry.TreeID)
	default:
		base.Mode = entry.Mode
		base.BlobId = entry.BlobID
		base.ContentHash = entry.ContentHash
		base.EntryFingerprint = FileEntryFingerprint(FileEntry{
			Path:        entry.Path,
			BlobID:      entry.BlobID,
			ContentHash: entry.ContentHash,
			Mode:        entry.Mode,
			Size:        entry.Size,
		})
	}
	return base
}

func upsertTestBlob(t *testing.T, ctx context.Context, store *DB, content string) (string, string) {
	t.Helper()
	data := []byte(content)
	blobID := objectid.BlobID(data)
	contentHash := objectid.RawContentHash(data)
	if err := store.Blobs().Upsert(ctx, blobID, contentHash, int64(len(data)), filesystem.BlobKey(contentHash)); err != nil {
		t.Fatal(err)
	}
	return blobID, contentHash
}

func upsertTestBlobObject(t *testing.T, ctx context.Context, store *DB, objectStore *filesystem.Store, content string) (string, string) {
	t.Helper()
	data := []byte(content)
	blobID := objectid.BlobID(data)
	contentHash := objectid.RawContentHash(data)
	key := filesystem.BlobKey(contentHash)
	if err := objectStore.Put(ctx, key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Blobs().Upsert(ctx, blobID, contentHash, int64(len(data)), key); err != nil {
		t.Fatal(err)
	}
	return blobID, contentHash
}
