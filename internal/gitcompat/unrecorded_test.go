package gitcompat

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// buildLazy projects n one-file commits lazily. When record is false the files
// have no recorded Git id, as every file uploaded before ids existed.
func buildLazy(t *testing.T, n int, record, dropOne bool) (head string, stats importStats, mem *memory.Stores, hashes []string, repo string, err error) {
	t.Helper()
	ctx := context.Background()
	store, serr := filesystem.New(t.TempDir())
	if serr != nil {
		t.Fatal(serr)
	}
	mem = memory.New()
	p := &Projector{blobs: mem.Blobs, objectStore: store, lazy: true}
	repo = filepath.Join(t.TempDir(), "acme", "p.git")
	if merr := os.MkdirAll(filepath.Dir(repo), 0o755); merr != nil {
		t.Fatal(merr)
	}
	if rerr := ensureProjectedRepo(ctx, repo); rerr != nil {
		t.Fatal(rerr)
	}
	var pending []pendingCommit
	ids := map[string]string{}
	for i := 0; i < n; i++ {
		data := bytes.Repeat([]byte{byte('a' + i%26)}, 100+i*37)
		data = append(data, []byte(strconv.Itoa(i))...)
		hash := objectid.RawContentHash(data)
		key := filesystem.BlobKey(hash)
		if !(dropOne && i == n/2) {
			if perr := store.Put(ctx, key, bytes.NewReader(data)); perr != nil {
				t.Fatal(perr)
			}
		}
		if uerr := mem.Blobs.Upsert(ctx, objectid.BlobID(data), hash, int64(len(data)), key); uerr != nil {
			t.Fatal(uerr)
		}
		ids[hash] = objectid.GitBlobID(data)
		hashes = append(hashes, hash)
		pending = append(pending, pendingCommit{
			native:  &corev1.Commit{Id: fmt.Sprintf("sha256:n%03d", i), Author: "u", CreatedAt: "2026-10-01T00:00:00Z", Message: "add"},
			changes: []fileChange{{Path: fmt.Sprintf("acme/p/f%03d.txt", i), Mode: "100644", ContentHash: hash, Size: int64(len(data))}},
		})
	}
	if record {
		if serr := mem.Blobs.SetGitBlobIDs(ctx, ids); serr != nil {
			t.Fatal(serr)
		}
	}
	state := &projectionState{Version: projectionVersion, Account: "acme", Slice: "p", Files: map[string]projectedFile{}}
	stats, err = p.appendLazy(ctx, repo, state, map[string]projectedFile{}, pending, map[string]string{}, map[string]string{}, hashes)
	return state.GitHead, stats, mem, hashes, repo, err
}

// Files without a recorded id are read, in parallel; the history is the same as
// when the ids were recorded, the files are in the repository, and the ids are
// recorded for the next build.
func TestLazyBuildReadsFilesWithoutRecordedIDs(t *testing.T) {
	const n = 60
	wantHead, _, _, _, _, err := buildLazy(t, n, true, false)
	if err != nil {
		t.Fatal(err)
	}
	head, stats, mem, hashes, repo, err := buildLazy(t, n, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if head != wantHead {
		t.Fatalf("head %s without recorded ids, %s with them", head, wantHead)
	}
	if stats.blobs != n {
		t.Fatalf("read %d files, want %d", stats.blobs, n)
	}
	if got := gitIn(t, repo, nil, "rev-list", "--objects", "--missing=print", head); bytes.Contains([]byte(got), []byte("?")) {
		t.Fatalf("files that were read must be in the repository:\n%s", got)
	}
	recorded, err := mem.Blobs.GitBlobIDs(context.Background(), hashes)
	if err != nil || len(recorded) != n {
		t.Fatalf("recorded %d ids (%v), want %d", len(recorded), err, n)
	}
	gitIn(t, repo, nil, "fsck", "--full")
}

func TestLazyBuildFailsCleanlyWhenAFileIsMissing(t *testing.T) {
	if _, _, _, _, _, err := buildLazy(t, 40, false, true); err == nil {
		t.Fatal("a file missing from the object store must fail the build")
	}
}
