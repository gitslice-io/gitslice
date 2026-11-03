package gitcompat

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingReads is an object store whose reads fail: a repository restored
// from a cover must not need to read any file.
type failingReads struct{ ObjectStore }

func (failingReads) Get(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("no reads expected")
}

func TestCompactBuildsAMirroredCoverThatServesWithoutReads(t *testing.T) {
	f := newLazyFixture(t)
	ctx := context.Background()
	f.p.locks = map[string]*sync.Mutex{}
	f.p.published = map[string]string{}
	f.p.mirror = NewPackMirror(f.store)
	if f.p.needsCover(f.repo) {
		t.Fatal("no cover before anyone cloned the whole files")
	}
	f.p.noteFullFetch(f.repo)
	if !f.p.needsCover(f.repo) {
		t.Fatal("a repository without a cover needs one after a full clone")
	}
	if err := f.p.compactRepo(ctx, "acme", "p", f.repo, time.Now()); err != nil {
		t.Fatal(err)
	}
	names, err := packNames(f.repo)
	if err != nil || len(names) != 1 {
		t.Fatalf("packs after compaction = %v, %v; want one", names, err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "objects", "pack", names[0]+".bitmap")); err != nil {
		t.Fatalf("the cover has no bitmap: %v", err)
	}
	if f.missing(t) != 0 {
		t.Fatalf("%d files missing from the cover", f.missing(t))
	}
	state := loadProjectionState(f.repo)
	if state.CoverHead != f.head || state.CompleteAt != f.head || len(state.HistoryPacks) != 1 || state.HistoryPacks[0] != names[0] {
		t.Fatalf("state = cover %s complete %s packs %v", state.CoverHead, state.CompleteAt, state.HistoryPacks)
	}
	if f.p.needsCover(f.repo) {
		t.Fatal("a fresh cover needs no other")
	}
	manifest, err := f.p.mirror.readManifest(ctx, "acme", "p")
	if err != nil || len(manifest.Packs) != 1 || manifest.Packs[0].Bitmap == nil {
		t.Fatalf("mirror manifest = %+v, %v; want one pack with its bitmap", manifest, err)
	}

	// Another instance restores the cover: every file is there, and a full
	// clone reads nothing from the store.
	other := filepath.Join(t.TempDir(), "acme", "p.git")
	restored, err := f.p.mirror.Restore(ctx, "acme", "p", other)
	if err != nil || !restored {
		t.Fatalf("restore = %v, %v", restored, err)
	}
	if _, err := os.Stat(filepath.Join(other, "objects", "pack", names[0]+".bitmap")); err != nil {
		t.Fatalf("the restored cover has no bitmap: %v", err)
	}
	reader := &Projector{blobs: f.mem.Blobs, objectStore: failingReads{f.store}, locks: map[string]*sync.Mutex{}}
	reader.setComplete(other, loadProjectionState(other).CompleteAt)
	if err := reader.HydrateFor(ctx, other, fetchBody("", 0, f.head)); err != nil {
		t.Fatalf("a full clone of a restored cover: %v", err)
	}
	gitIn(t, other, nil, "fsck", "--full")
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, "", nil, "clone", "-q", "--bare", "file://"+other, clone)
	for path, want := range f.contents {
		if got := gitIn(t, clone, nil, "show", "main:"+path); got != strings.TrimSpace(string(want)) {
			t.Fatalf("%s: %d bytes in the clone, want %d", path, len(got), len(want))
		}
	}
}

func TestNeedsCoverAfterManyPacks(t *testing.T) {
	f := newLazyFixture(t)
	f.p.locks = map[string]*sync.Mutex{}
	f.p.published = map[string]string{}
	f.p.mirror = NewPackMirror(f.store)
	if err := f.p.compactRepo(context.Background(), "acme", "p", f.repo, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.p.noteFullFetch(f.repo)
	dir := filepath.Join(f.repo, "objects", "pack")
	for i := 0; i < coverMaxPacks; i++ {
		name := filepath.Join(dir, "pack-fake"+itoa(i))
		if err := os.WriteFile(name+".pack", nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !f.p.needsCover(f.repo) {
		t.Fatalf("%d packs on a cover should ask for another", coverMaxPacks+1)
	}
}

func TestNoCoverForAProjectionTooBigForTheDisk(t *testing.T) {
	f := newLazyFixture(t)
	f.p.noteFullFetch(f.repo)
	saved := coverMaxBytes
	coverMaxBytes = 1
	defer func() { coverMaxBytes = saved }()
	if f.p.needsCover(f.repo) {
		t.Fatal("a projection larger than coverMaxBytes should not be covered")
	}
}
