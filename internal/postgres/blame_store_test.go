package postgres

import (
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestBlameSnapshotStore(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	blames := store.Blames()
	owners := []string{"sha256:a", "sha256:a", "sha256:b", "sha256:a", "sha256:c", "sha256:c"}
	snapshot := storage.BlameSnapshot{CommitID: "sha256:c", Path: "/acme/payment/big.go", ContentHash: "sha256:content", Owners: owners}
	if err := blames.PutBlameSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	// Storing it again keeps the first.
	again := snapshot
	again.Owners = []string{"sha256:x"}
	if err := blames.PutBlameSnapshot(ctx, again); err != nil {
		t.Fatal(err)
	}
	got, err := blames.BlameSnapshots(ctx, []string{"sha256:c", "sha256:missing"})
	if err != nil {
		t.Fatal(err)
	}
	read := got["sha256:c"]["/acme/payment/big.go"]
	if read == nil || read.ContentHash != "sha256:content" || read.Truncated || strings.Join(read.Owners, ",") != strings.Join(owners, ",") || len(got) != 1 {
		t.Fatalf("snapshots = %+v", got)
	}
	empty, err := blames.BlameSnapshots(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no ids: %v, %v", empty, err)
	}
}

func TestBlameSnapshotEncodingIsCompact(t *testing.T) {
	owners := make([]string, 14000)
	for i := range owners {
		owners[i] = "sha256:" + strings.Repeat(string(rune('a'+i/1000)), 64)
	}
	data, err := encodeBlameSnapshot(storage.BlameSnapshot{Owners: owners})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 4096 {
		t.Fatalf("a 14,000-line snapshot in 14 runs takes %d bytes", len(data))
	}
	decoded, err := decodeBlameSnapshot("c", "/p", data)
	if err != nil || strings.Join(decoded.Owners, ",") != strings.Join(owners, ",") {
		t.Fatalf("round trip: %v", err)
	}
}
