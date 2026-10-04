package gitcompat

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type lazyBuild struct {
	head   string
	stats  importStats
	mem    *memory.Stores
	hashes []string
	repo   string
	err    error
}

// buildLazy projects n one-file commits lazily. When record is false the files
// have no recorded Git id, as every file uploaded before ids existed. dropOne
// leaves one file out of the object store.
func buildLazy(t *testing.T, n int, record, dropOne bool) lazyBuild {
	t.Helper()
	ctx := context.Background()
	store, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := lazyBuild{mem: memory.New(), repo: filepath.Join(t.TempDir(), "acme", "p.git")}
	p := &Projector{blobs: b.mem.Blobs, objectStore: store}
	if err := os.MkdirAll(filepath.Dir(b.repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjectedRepo(ctx, b.repo); err != nil {
		t.Fatal(err)
	}
	var pending []pendingCommit
	ids := map[string]string{}
	for i := 0; i < n; i++ {
		data := append(bytes.Repeat([]byte{byte('a' + i%26)}, 100+i*37), strconv.Itoa(i)...)
		hash := objectid.RawContentHash(data)
		key := filesystem.BlobKey(hash)
		if !dropOne || i != n/2 {
			if err := store.Put(ctx, key, bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.mem.Blobs.Upsert(ctx, objectid.BlobID(data), hash, int64(len(data)), key); err != nil {
			t.Fatal(err)
		}
		ids[hash] = objectid.GitBlobID(data)
		b.hashes = append(b.hashes, hash)
		pending = append(pending, pendingCommit{
			native:  &corev1.Commit{Id: fmt.Sprintf("sha256:n%03d", i), Author: "u", CreatedAt: "2026-10-01T00:00:00Z", Message: "add"},
			changes: []fileChange{{Path: fmt.Sprintf("acme/p/f%03d.txt", i), Mode: "100644", ContentHash: hash, Size: int64(len(data))}},
		})
	}
	if record {
		if err := b.mem.Blobs.SetGitBlobIDs(ctx, ids); err != nil {
			t.Fatal(err)
		}
	}
	state := &projectionState{Version: projectionVersion, Account: "acme", Slice: "p", Files: map[string]projectedFile{}}
	b.stats, b.err = p.appendLazy(ctx, b.repo, state, map[string]projectedFile{}, pending, map[string]string{}, map[string]string{}, b.hashes)
	b.head = state.GitHead
	return b
}

// Files without a recorded id are read, in parallel; the history is the same as
// when the ids were recorded, the files are in the repository, and the ids are
// recorded for the next build.
func TestLazyBuildReadsFilesWithoutRecordedIDs(t *testing.T) {
	const n = 60
	recorded := buildLazy(t, n, true, false)
	unrecorded := buildLazy(t, n, false, false)
	if recorded.err != nil || unrecorded.err != nil {
		t.Fatalf("builds failed: %v, %v", recorded.err, unrecorded.err)
	}
	if unrecorded.head != recorded.head {
		t.Fatalf("head %s without recorded ids, %s with them", unrecorded.head, recorded.head)
	}
	if unrecorded.stats.blobs != n {
		t.Fatalf("read %d files, want %d", unrecorded.stats.blobs, n)
	}
	if got := gitIn(t, unrecorded.repo, nil, "rev-list", "--objects", "--missing=print", unrecorded.head); strings.Contains(got, "?") {
		t.Fatalf("files that were read must be in the repository:\n%s", got)
	}
	ids, err := unrecorded.mem.Blobs.GitBlobIDs(context.Background(), unrecorded.hashes)
	if err != nil || len(ids) != n {
		t.Fatalf("recorded %d ids (%v), want %d", len(ids), err, n)
	}
	gitIn(t, unrecorded.repo, nil, "fsck", "--full")
}

func TestLazyBuildFailsCleanlyWhenAFileIsMissing(t *testing.T) {
	if b := buildLazy(t, 40, false, true); b.err == nil {
		t.Fatal("a file missing from the object store must fail the build")
	}
}
