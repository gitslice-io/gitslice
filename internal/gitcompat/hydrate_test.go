package gitcompat

import (
	"bytes"
	"context"
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

// lazyFixture builds a lazily projected repository of three files whose
// contents are in an object store but not in the repository.
type lazyFixture struct {
	p        *Projector
	repo     string
	head     string
	contents map[string][]byte // path → bytes
	store    *filesystem.Store
	mem      *memory.Stores
	rootOf   string
}

func newLazyFixture(t *testing.T) *lazyFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	mem := memory.New()
	p := &Projector{blobs: mem.Blobs, objectStore: store}
	repo := filepath.Join(t.TempDir(), "acme", "p.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjectedRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{
		"acme/p/a.txt":     []byte("alpha\n"),
		"acme/p/dir/b.txt": bytes.Repeat([]byte("b"), 200_000),
		"acme/p/dir/c.txt": []byte(""),
	}
	var pending []pendingCommit
	var need []string
	ids := map[string]string{}
	i := 0
	for path, data := range contents {
		i++
		hash := objectid.RawContentHash(data)
		key := filesystem.BlobKey(hash)
		if err := store.Put(ctx, key, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		if err := mem.Blobs.Upsert(ctx, objectid.BlobID(data), hash, int64(len(data)), key); err != nil {
			t.Fatal(err)
		}
		ids[hash] = objectid.GitBlobID(data)
		need = append(need, hash)
		pending = append(pending, pendingCommit{
			native:  &corev1.Commit{Id: "sha256:n" + string(rune('0'+i)), Author: "u", CreatedAt: "2026-10-01T00:00:00Z", Message: "add " + path},
			changes: []fileChange{{Path: path, Mode: "100644", ContentHash: hash, Size: int64(len(data))}},
		})
	}
	if err := mem.Blobs.SetGitBlobIDs(ctx, ids); err != nil {
		t.Fatal(err)
	}
	state := &projectionState{Version: projectionVersion, Account: "acme", Slice: "p", Files: map[string]projectedFile{}}
	files := map[string]projectedFile{}
	if _, err := p.appendLazy(ctx, repo, state, files, pending, map[string]string{}, map[string]string{}, need); err != nil {
		t.Fatal(err)
	}
	return &lazyFixture{p: p, repo: repo, head: state.GitHead, contents: contents, store: store, mem: mem, rootOf: root}
}

func (f *lazyFixture) missing(t *testing.T) int {
	t.Helper()
	return strings.Count(gitIn(t, f.repo, nil, "rev-list", "--objects", "--missing=print", f.head), "?")
}

func fetchBody(filter string, depth int, wants ...string) []byte {
	lines := []string{"command=fetch\n", "DELIM", "ofs-delta\n"}
	for _, w := range wants {
		lines = append(lines, "want "+w+"\n")
	}
	if filter != "" {
		lines = append(lines, "filter "+filter+"\n")
	}
	if depth > 0 {
		lines = append(lines, "deepen "+itoa(depth)+"\n")
	}
	lines = append(lines, "done\n", "FLUSH")
	return []byte(pkt(lines...))
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestHydrateWritesWhatAFullCloneNeeds(t *testing.T) {
	f := newLazyFixture(t)
	ctx := context.Background()
	if f.missing(t) != 3 {
		t.Fatalf("expected three files missing from a lazily built repository, got %d", f.missing(t))
	}
	// A blobless fetch needs no contents.
	if err := f.p.HydrateFor(ctx, f.repo, fetchBody("blob:none", 0, f.head)); err != nil || f.missing(t) != 3 {
		t.Fatalf("blobless fetch: err=%v missing=%d", err, f.missing(t))
	}
	// A full fetch writes them all.
	if err := f.p.HydrateFor(ctx, f.repo, fetchBody("", 0, f.head)); err != nil {
		t.Fatal(err)
	}
	if f.missing(t) != 0 {
		t.Fatalf("%d files still missing after a full fetch", f.missing(t))
	}
	gitIn(t, f.repo, nil, "fsck", "--full")
	for path, want := range f.contents {
		if got := gitIn(t, f.repo, nil, "show", f.head+":"+path); got != strings.TrimSpace(string(want)) {
			t.Fatalf("%s: %d bytes, want %d", path, len(got), len(want))
		}
	}
	// Nothing is left to do, and the answer is remembered.
	if err := f.p.HydrateFor(ctx, f.repo, fetchBody("", 0, f.head)); err != nil {
		t.Fatal(err)
	}
	if f.p.completeAt(f.repo) != f.head {
		t.Fatal("a complete head should be remembered")
	}
}

func TestHydrateFetchesOneFileByID(t *testing.T) {
	f := newLazyFixture(t)
	want := objectid.GitBlobID(f.contents["acme/p/a.txt"])
	if err := f.p.HydrateFor(context.Background(), f.repo, fetchBody("", 0, want)); err != nil {
		t.Fatal(err)
	}
	if f.missing(t) != 2 {
		t.Fatalf("only the requested file should have been written, %d are missing", f.missing(t))
	}
	if got := gitIn(t, f.repo, nil, "cat-file", "-p", want); got != "alpha" {
		t.Fatalf("hydrated file = %q", got)
	}
}

func TestHydrateShallowFetch(t *testing.T) {
	f := newLazyFixture(t)
	// Depth 1 is the head commit's whole tree; the history is linear and all
	// three files exist at the head, so all three are needed.
	if err := f.p.HydrateFor(context.Background(), f.repo, fetchBody("", 1, f.head)); err != nil {
		t.Fatal(err)
	}
	if f.missing(t) != 0 {
		t.Fatalf("%d missing after a depth-1 fetch", f.missing(t))
	}
}

func TestHydrateRefusesContentThatDoesNotMatchItsID(t *testing.T) {
	f := newLazyFixture(t)
	// Replace the stored bytes of a.txt with other bytes of the same length.
	data := f.contents["acme/p/a.txt"]
	key := filesystem.BlobKey(objectid.RawContentHash(data))
	if err := os.WriteFile(filepath.Join(f.storeRoot(t), filepath.FromSlash(key)), []byte("ALPHA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := f.p.HydrateFor(context.Background(), f.repo, fetchBody("", 0, f.head))
	if err == nil || !strings.Contains(err.Error(), "hashes to") {
		t.Fatalf("corrupt content must be refused, got %v", err)
	}
	if f.missing(t) < 1 {
		t.Fatal("the corrupt file must not have been written")
	}
}

func TestHydrateSaysWhenAFileHasNoRecord(t *testing.T) {
	f := newLazyFixture(t)
	missing := objectid.GitBlobID([]byte("never uploaded\n"))
	err := f.p.HydrateFor(context.Background(), f.repo, fetchBody("", 0, missing))
	if err == nil || !strings.Contains(err.Error(), "no record") {
		t.Fatalf("an unknown file must be reported, got %v", err)
	}
}

func (f *lazyFixture) storeRoot(t *testing.T) string {
	t.Helper()
	return f.rootOf
}
