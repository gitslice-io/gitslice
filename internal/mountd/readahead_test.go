package mountd

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
)

type readAheadFixture struct {
	f       *Fetcher
	order   []lazyfs.BaseEntry
	mu      sync.Mutex
	fetched map[string]int
}

func newReadAheadFixture(t *testing.T, dirs, perDir int) *readAheadFixture {
	t.Helper()
	cache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := map[string][]byte{}
	var entries []lazyfs.BaseEntry
	for d := 0; d < dirs; d++ {
		for i := 0; i < perDir; i++ {
			p := fmt.Sprintf("/src/pkg%02d/file%03d.go", d, i)
			data := []byte("package x // " + p)
			obj, err := server.PutBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			hash := obj.ContentHash
			content[hash] = data
			entries = append(entries, lazyfs.BaseEntry{Path: p, Kind: lazyfs.KindFile, Size: 100 << 10, ContentHash: hash})
		}
	}
	m, err := lazyfs.NewManifest("commit1", entries)
	if err != nil {
		t.Fatal(err)
	}
	x := &readAheadFixture{fetched: map[string]int{}}
	x.f = NewFetcher(nil, nil, cache, m, func(ctx context.Context, commitID string, batch []lazyfs.BaseEntry) (int, int64, error) {
		time.Sleep(2 * time.Millisecond)
		for _, entry := range batch {
			if _, err := cache.PutBytes(content[entry.ContentHash]); err != nil {
				return 0, 0, err
			}
			x.mu.Lock()
			x.fetched[entry.Path]++
			x.mu.Unlock()
		}
		return len(batch), 0, nil
	})
	x.order = WalkOrder(m)
	return x
}

func (x *readAheadFixture) open(t *testing.T, entry lazyfs.BaseEntry) bool {
	t.Helper()
	_, fetched, err := x.f.Fetch(context.Background(), entry.Path, lazyfs.Attr{Kind: lazyfs.KindFile, Size: entry.Size, ContentHash: entry.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	return fetched
}

func (x *readAheadFixture) totals() (files, twice int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, n := range x.fetched {
		files++
		if n > 1 {
			twice++
		}
	}
	return files, twice
}

func TestWalkOrderIsReaddirOrder(t *testing.T) {
	var entries []lazyfs.BaseEntry
	for _, p := range []string{"/b.txt", "/a-b/z.txt", "/a/y.txt", "/a/x/w.txt"} {
		entries = append(entries, lazyfs.BaseEntry{Path: p, Kind: lazyfs.KindFile, ContentHash: "sha256:" + p})
	}
	m, err := lazyfs.NewManifest("c", entries)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range WalkOrder(m) {
		got = append(got, entry.Path)
	}
	want := []string{"/a/x/w.txt", "/a/y.txt", "/a-b/z.txt", "/b.txt"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("WalkOrder = %v, want %v", got, want)
	}
}

func TestReadAheadRunsAheadOfAWalk(t *testing.T) {
	x := newReadAheadFixture(t, 20, 30)
	misses := 0
	for _, entry := range x.order {
		if x.open(t, entry) {
			misses++
		}
	}
	if misses > 40 {
		t.Fatalf("%d of %d opens of a walk were misses; read-ahead should have fetched most first", misses, len(x.order))
	}
	files, twice := x.totals()
	if files != len(x.order) || twice != 0 {
		t.Fatalf("fetched %d distinct files (%d twice), want each of %d once", files, twice, len(x.order))
	}
}

func TestReadAheadRunsAheadOfAWalkFromTheEnd(t *testing.T) {
	x := newReadAheadFixture(t, 20, 30)
	misses := 0
	for i := len(x.order) - 1; i >= 0; i-- {
		if x.open(t, x.order[i]) {
			misses++
		}
	}
	if misses > 40 {
		t.Fatalf("%d of %d opens of a backward walk were misses", misses, len(x.order))
	}
	if files, twice := x.totals(); files != len(x.order) || twice != 0 {
		t.Fatalf("fetched %d distinct files (%d twice), want each of %d once", files, twice, len(x.order))
	}
}

func TestReadAheadToleratesParallelReaders(t *testing.T) {
	x := newReadAheadFixture(t, 20, 30)
	var wg sync.WaitGroup
	var mu sync.Mutex
	misses := 0
	next := make(chan lazyfs.BaseEntry)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range next {
				if x.open(t, entry) {
					mu.Lock()
					misses++
					mu.Unlock()
				}
			}
		}()
	}
	for _, entry := range x.order {
		next <- entry
	}
	close(next)
	wg.Wait()
	if misses > 80 {
		t.Fatalf("%d of %d opens by parallel readers were misses", misses, len(x.order))
	}
	if _, twice := x.totals(); twice != 0 {
		t.Fatalf("%d files were fetched twice", twice)
	}
}

func TestReadAheadStaysOffForScatteredReads(t *testing.T) {
	x := newReadAheadFixture(t, 20, 30)
	r := rand.New(rand.NewSource(1))
	opened := map[string]bool{}
	for _, i := range r.Perm(len(x.order))[:100] {
		x.open(t, x.order[i])
		opened[x.order[i].Path] = true
	}
	time.Sleep(50 * time.Millisecond)
	x.mu.Lock()
	defer x.mu.Unlock()
	for p := range x.fetched {
		if !opened[p] {
			t.Fatalf("scattered reads fetched %s, which nothing opened (%d fetched for %d opens)", p, len(x.fetched), len(opened))
		}
	}
}

func TestReadAheadCanBeTurnedOff(t *testing.T) {
	t.Setenv("GS_READAHEAD", "off")
	x := newReadAheadFixture(t, 4, 30)
	misses := 0
	for _, entry := range x.order {
		if x.open(t, entry) {
			misses++
		}
	}
	if misses != len(x.order) {
		t.Fatalf("with GS_READAHEAD=off, %d of %d opens were misses, want all", misses, len(x.order))
	}
}
