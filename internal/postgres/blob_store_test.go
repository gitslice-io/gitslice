package postgres

import (
	"reflect"
	"testing"
)

func TestBlobStoreSliceAssociationsAndPathsByContentHash(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	_, firstHash := upsertTestBlob(t, ctx, store, "first blob\n")
	secondBlobID, secondHash := upsertTestBlob(t, ctx, store, "second blob\n")

	if err := store.Blobs().AssociateSlices(ctx, "slice_acme_payment", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Blobs().AssociateSlices(ctx, "slice_acme_payment", []string{firstHash, firstHash}); err != nil {
		t.Fatal(err)
	}
	if err := store.Blobs().AssociateSlices(ctx, "slice_acme_payment", []string{firstHash}); err != nil {
		t.Fatal(err)
	}

	associations, err := store.Blobs().SliceAssociations(ctx, "slice_acme_payment", []string{firstHash, secondHash})
	if err != nil {
		t.Fatal(err)
	}
	if !associations[firstHash] || associations[secondHash] {
		t.Fatalf("payment associations = %#v, want only %s", associations, firstHash)
	}
	otherAssociations, err := store.Blobs().SliceAssociations(ctx, "slice_acme_backend", []string{firstHash})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherAssociations) != 0 {
		t.Fatalf("backend associations = %#v, want none", otherAssociations)
	}
	emptyAssociations, err := store.Blobs().SliceAssociations(ctx, "slice_acme_payment", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyAssociations) != 0 {
		t.Fatalf("empty associations = %#v, want none", emptyAssociations)
	}

	if _, err := store.db.ExecContext(ctx, `
		insert into path_heads(path, exists, entry_fingerprint, blob_id, content_hash, mode, size, updated_at)
		values
			('/acme/backend/a.txt', true, 'fingerprint-a', $1, $2, 33188, 12, now()),
			('/acme/backend/b.txt', true, 'fingerprint-b', $1, $2, 33188, 12, now())
	`, secondBlobID, secondHash); err != nil {
		t.Fatal(err)
	}
	pathsByHash, err := store.Blobs().PathsByContentHash(ctx, []string{firstHash, secondHash, "sha256:missing"})
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"/acme/backend/a.txt", "/acme/backend/b.txt"}
	if !reflect.DeepEqual(pathsByHash[secondHash], wantPaths) || len(pathsByHash) != 1 {
		t.Fatalf("paths by hash = %#v, want %s at %#v", pathsByHash, secondHash, wantPaths)
	}
	emptyPaths, err := store.Blobs().PathsByContentHash(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyPaths) != 0 {
		t.Fatalf("empty paths = %#v, want none", emptyPaths)
	}
}

func TestBlobStoreGitBlobIDs(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	for i, hash := range []string{"sha256:aa", "sha256:bb", "sha256:cc"} {
		if err := store.Blobs().Upsert(ctx, "blob_"+hash, hash, int64(i+1), "blobs/"+hash); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Blobs().SetGitBlobIDs(ctx, map[string]string{"sha256:aa": "1111111111111111111111111111111111111111"}); err != nil {
		t.Fatal(err)
	}
	// Setting again, and setting an unknown blob, are harmless.
	if err := store.Blobs().SetGitBlobIDs(ctx, map[string]string{"sha256:aa": "1111111111111111111111111111111111111111", "sha256:zz": "2222222222222222222222222222222222222222"}); err != nil {
		t.Fatal(err)
	}
	ids, err := store.Blobs().GitBlobIDs(ctx, []string{"sha256:aa", "sha256:bb", "sha256:zz"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids["sha256:aa"] != "1111111111111111111111111111111111111111" {
		t.Fatalf("git ids = %#v, want only sha256:aa", ids)
	}
	missing, err := store.Blobs().ListMissingGitBlobIDs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 || missing[0].ContentHash != "sha256:bb" || missing[1].ContentHash != "sha256:cc" || missing[0].StorageLocation != "blobs/sha256:bb" || missing[1].Size != 3 {
		t.Fatalf("missing = %#v, want sha256:bb and sha256:cc", missing)
	}
	if limited, _ := store.Blobs().ListMissingGitBlobIDs(ctx, 1); len(limited) != 1 {
		t.Fatalf("limit 1 returned %d blobs", len(limited))
	}
}
