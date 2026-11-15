package cli

import (
	"os"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
)

// Read-ahead. A program that walks the tree (grep -r, a build, an indexer)
// reads files in the order the directories list them, or in the reverse of
// it (rg takes a directory's entries from the end), and each uncached file
// costs a server round trip. When reads follow that order, the mount fetches
// the files that come next before they are asked for, so the walk finds them
// in the cache. A window that starts small and doubles keeps a short burst
// from fetching much, while a real walk soon runs ahead of the reader.
//
// It looks only at the order of opens, never at a file's content, and it
// stops as soon as the reads stop following the order.
const (
	readAheadTrigger     = 6        // opens in walk order before it starts
	readAheadJitter      = 64       // how far from the furthest open still counts as in order (parallel readers)
	readAheadFirstWindow = 32       // files fetched ahead at first
	readAheadMaxWindow   = 1024     // and at most
	readAheadMaxFile     = 1 << 20  // larger files are left to the reader
	readAheadMaxBytes    = 32 << 20 // fetched ahead in one step, at most
)

type readAhead struct {
	mu      sync.Mutex
	order   []lazyfs.BaseEntry // the base's files in walk order, built when first needed
	index   map[string]int
	last    int  // the previous open, to ignore a file opened again
	start   int  // the first open of the current walk
	lo, hi  int  // the lowest and highest opens of the current walk
	run     int  // opens in order so far
	aheadLo int  // the lowest index handed to a fetch (a walk from the end)
	aheadHi int  // the highest index handed to a fetch (a walk from the start)
	window  int  // files to stay ahead by
	fetcher bool // a read-ahead fetch is running
}

// reset forgets the listing and the walk, after the base moved.
func (a *readAhead) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.order, a.index = nil, nil
	a.restart(-1)
}

// restart begins a new walk at index i (or none, with -1).
func (a *readAhead) restart(i int) {
	a.last, a.start, a.lo, a.hi, a.run = i, i, i, i, 0
	a.aheadLo, a.aheadHi, a.window = i, i, readAheadFirstWindow
}

// walkOrder lists the base's files as a depth-first walk of sorted directories
// does, which is the order readdir gives them.
func walkOrder(m *lazyfs.Manifest) []lazyfs.BaseEntry {
	var out []lazyfs.BaseEntry
	var walk func(dir string)
	walk = func(dir string) {
		for _, name := range m.Children(dir) {
			child := joinGlobalPath(dir, name)
			entry, ok := m.Lookup(child)
			if !ok {
				continue
			}
			if entry.Kind == lazyfs.KindDir {
				walk(child)
			} else if entry.Kind == lazyfs.KindFile {
				out = append(out, entry)
			}
		}
	}
	walk("/")
	return out
}

// observeRead notes that the file at p is being opened and, when the opens are
// walking the tree, starts fetching the files ahead of them.
func (f *mountFetcher) observeRead(p string) {
	if os.Getenv("GS_READAHEAD") == "off" {
		return
	}
	// f.mu before a.mu, as setBase takes them.
	f.mu.Lock()
	manifest := f.manifest
	f.mu.Unlock()
	if manifest == nil {
		return
	}
	a := &f.ahead
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.window == 0 {
		a.restart(-1)
	}
	if a.index == nil {
		a.order = walkOrder(manifest)
		a.index = make(map[string]int, len(a.order))
		for i, entry := range a.order {
			a.index[entry.Path] = i
		}
	}
	i, ok := a.index[p]
	if !ok || i == a.last {
		return
	}
	if a.start < 0 || i < a.lo-readAheadJitter || i > a.hi+readAheadJitter {
		// Not following the order: start over from here.
		a.restart(i)
		return
	}
	a.last = i
	a.lo, a.hi = min(a.lo, i), max(a.hi, i)
	a.run++
	if a.run < readAheadTrigger || a.fetcher {
		return
	}
	// A walk goes either way: rg, for one, takes a directory's entries from
	// the end.
	var entries []lazyfs.BaseEntry
	if a.hi-a.start >= a.start-a.lo {
		if a.aheadHi >= a.hi+a.window/2 {
			return
		}
		from, to := max(a.aheadHi, a.hi)+1, min(a.hi+a.window, len(a.order)-1)
		if from > to {
			return
		}
		entries = append(entries, a.order[from:to+1]...)
		a.aheadHi = to
	} else {
		if a.aheadLo <= a.lo-a.window/2 {
			return
		}
		from, to := min(a.aheadLo, a.lo)-1, max(a.lo-a.window, 0)
		if from < to {
			return
		}
		for j := from; j >= to; j-- {
			entries = append(entries, a.order[j])
		}
		a.aheadLo = to
	}
	a.window = min(a.window*2, readAheadMaxWindow)
	a.fetcher = true
	go f.readAheadFetch(entries)
}

// readAheadFetch fetches entries into the cache, in the same batches and with
// the same bookkeeping as a miss, so a reader that catches up with one waits
// for it instead of fetching the file a second time.
func (f *mountFetcher) readAheadFetch(entries []lazyfs.BaseEntry) {
	defer func() {
		f.ahead.mu.Lock()
		f.ahead.fetcher = false
		f.ahead.mu.Unlock()
	}()
	type job struct {
		batch []lazyfs.BaseEntry
		call  *fetchCall
	}
	f.mu.Lock()
	if time.Until(f.downUntil) > 0 {
		f.mu.Unlock()
		return
	}
	commitID := f.commitID
	var jobs []job
	var cur []lazyfs.BaseEntry
	var curBytes, total int64
	flush := func() {
		if len(cur) == 0 {
			return
		}
		call := &fetchCall{done: make(chan struct{})}
		for _, entry := range cur {
			f.inflight[entry.ContentHash] = call
		}
		jobs = append(jobs, job{cur, call})
		cur, curBytes = nil, 0
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Size > readAheadMaxFile || seen[entry.ContentHash] || f.inflight[entry.ContentHash] != nil || f.cache.Exists(entry.ContentHash) {
			continue
		}
		if total+entry.Size > readAheadMaxBytes {
			break
		}
		seen[entry.ContentHash] = true
		if len(cur) >= mountBatchMaxFiles || curBytes+entry.Size > mountBatchMaxBytes {
			flush()
		}
		cur = append(cur, entry)
		curBytes += entry.Size
		total += entry.Size
	}
	flush()
	f.mu.Unlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, mountFetchConcurrency)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			_ = f.runBatch(commitID, j.batch, j.call)
		}(j)
	}
	wg.Wait()
}
