package clientcache

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// TestPruneRemovesLeastRecentlyUsed: over the limit, Prune removes the
// objects used longest ago until the cache is at its target, and keeps
// pinned objects and recently used ones.
func TestPruneRemovesLeastRecentlyUsed(t *testing.T) {
	cache, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(fill byte, age time.Duration) Object {
		t.Helper()
		object, err := cache.PutBytes(bytes.Repeat([]byte{fill}, 100))
		if err != nil {
			t.Fatal(err)
		}
		p, _ := cache.Path(object.ContentHash)
		at := time.Now().Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return object
	}
	oldest := put('a', 30*24*time.Hour)
	pinned := put('b', 20*24*time.Hour)
	older := put('c', 10*24*time.Hour)
	old := put('d', 5*24*time.Hour)
	recent := put('e', time.Hour)

	// Under the limit: nothing goes.
	if res, err := cache.Prune(PruneOptions{MaxBytes: 1000}); err != nil || res.Removed != 0 || res.Objects != 5 || res.Bytes != 500 {
		t.Fatalf("under the limit: %+v, %v", res, err)
	}
	keep := func(hash string) bool { return hash == pinned.ContentHash }
	res, err := cache.Prune(PruneOptions{MaxBytes: 400, TargetBytes: 250, Keep: keep})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 3 || res.RemovedBytes != 300 || res.Kept != 1 {
		t.Fatalf("prune = %+v, want 3 removed and 1 pinned", res)
	}
	for _, gone := range []Object{oldest, older, old} {
		if cache.Exists(gone.ContentHash) {
			t.Fatalf("%s is still there", gone.ContentHash)
		}
	}
	for _, kept := range []Object{pinned, recent} {
		if !cache.Exists(kept.ContentHash) {
			t.Fatalf("%s was removed", kept.ContentHash)
		}
	}

	// What was used recently stays even when the cache is still over.
	res, err = cache.Prune(PruneOptions{MaxBytes: 10, Keep: keep})
	if err != nil || res.Removed != 0 {
		t.Fatalf("recent objects: %+v, %v", res, err)
	}
}

// TestExistsMarksUse: checking for a stale object refreshes its last use, so
// Prune does not take something a caller is about to read.
func TestExistsMarksUse(t *testing.T) {
	cache, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	object, err := cache.PutBytes([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cache.Path(object.ContentHash)
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}
	if !cache.Exists(object.ContentHash) {
		t.Fatal("object missing")
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("last use = %v, want now", info.ModTime())
	}
	if res, _ := cache.Prune(PruneOptions{MaxBytes: 0}); res.Removed != 0 {
		t.Fatal("no limit removed something")
	}
}
