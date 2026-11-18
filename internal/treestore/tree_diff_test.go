package treestore

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestDiffTreeMatchesListings: applying DiffTree's result to the old listing
// gives the new one, for random trees and random changes between them, and
// the result names nothing that did not change.
func TestDiffTreeMatchesListings(t *testing.T) {
	ctx := context.Background()
	store := New(newMemoryObjectStore())
	if err := store.EnsureEmptyRoot(ctx); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	dirs := []string{"/acme/app", "/acme/app/src", "/acme/app/src/deep", "/acme/app/docs", "/acme/app/x", "/acme/other"}
	randomPath := func() string {
		return fmt.Sprintf("%s/f%d", dirs[rng.Intn(len(dirs))], rng.Intn(6))
	}
	file := func(p string) *FileEntry {
		v := rng.Intn(3)
		mode := uint32(0o100644)
		if rng.Intn(5) == 0 {
			mode = 0o100755
		}
		return &FileEntry{Path: p, BlobID: fmt.Sprintf("blob-%d", v), ContentHash: fmt.Sprintf("sha256:%d", v), Mode: mode, Size: int64(v)}
	}
	for round := 0; round < 200; round++ {
		var edits []FileEdit
		for i := 0; i < 20; i++ {
			p := randomPath()
			edits = append(edits, FileEdit{Op: "upsert", Path: p, File: file(p)})
		}
		oldRoot, err := store.ApplyEdits(ctx, EmptyRootID(), edits)
		if err != nil {
			t.Fatal(err)
		}
		var changes []FileEdit
		for i := rng.Intn(6); i > 0; i-- {
			p := randomPath()
			if rng.Intn(2) == 0 {
				changes = append(changes, FileEdit{Op: "upsert", Path: p, File: file(p)})
			} else if _, err := store.GetFile(ctx, oldRoot, p); err == nil {
				changes = append(changes, FileEdit{Op: "delete", Path: p})
			}
		}
		if rng.Intn(8) == 0 { // a directory goes away, or becomes a file
			changes = append(changes, FileEdit{Op: "delete", Path: "/acme/app/src"})
			if rng.Intn(2) == 0 {
				changes = append(changes, FileEdit{Op: "upsert", Path: "/acme/app/src", File: file("/acme/app/src")})
			}
		}
		newRoot := oldRoot
		if len(changes) > 0 {
			if newRoot, err = store.ApplyEdits(ctx, oldRoot, changes); err != nil {
				t.Fatal(err)
			}
		}
		for _, at := range []string{"/", "/acme/app"} {
			prev, err := store.ListTree(ctx, oldRoot, at)
			if err != nil {
				t.Fatal(err)
			}
			next, err := store.ListTree(ctx, newRoot, at)
			if err != nil {
				t.Fatal(err)
			}
			changed, removed, ok, err := store.DiffTree(ctx, oldRoot, newRoot, at)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				continue // /acme/app may have become a file; the caller compares listings
			}
			got := applyDiff(prev, changed, removed)
			if want := comparable(next); !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d at %s: applied diff\n%v\nwant\n%v\n(changed %v, removed %v)", round, at, got, want, changed, removed)
			}
			before := map[string]TreeEntry{}
			for _, e := range prev {
				before[e.Path] = e
			}
			for _, e := range changed {
				if old, had := before[e.Path]; had && old.Kind == e.Kind && (e.Kind == "directory" || old == e) {
					t.Fatalf("round %d: %s is reported changed but is the same", round, e.Path)
				}
			}
			for i := 1; i < len(removed); i++ {
				if strings.HasPrefix(removed[i], removed[i-1]+"/") {
					t.Fatalf("round %d: %s is under %s, which is removed already", round, removed[i], removed[i-1])
				}
			}
		}
	}
}

func applyDiff(prev, changed []TreeEntry, removed []string) []string {
	byPath := map[string]TreeEntry{}
outer:
	for _, e := range prev {
		for _, r := range removed {
			if e.Path == r || strings.HasPrefix(e.Path, r+"/") {
				continue outer
			}
		}
		byPath[e.Path] = e
	}
	for _, e := range changed {
		byPath[e.Path] = e
	}
	var out []TreeEntry
	for _, e := range byPath {
		out = append(out, e)
	}
	return comparable(out)
}

// comparable renders a listing without directory tree ids, which a client
// does not keep.
func comparable(entries []TreeEntry) []string {
	var out []string
	for _, e := range entries {
		if e.Kind == "directory" {
			out = append(out, e.Path+" dir")
			continue
		}
		out = append(out, fmt.Sprintf("%s %s %o %s %d", e.Path, e.Kind, e.Mode, e.ContentHash, e.Size))
	}
	sort.Strings(out)
	return out
}
