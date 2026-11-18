package mountd

import (
	"os"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/lazyfs"
)

const (
	readAheadTrigger     = 6
	readAheadJitter      = 64
	readAheadFirstWindow = 32
	readAheadMaxWindow   = 1024
	readAheadMaxFile     = 1 << 20
	readAheadMaxBytes    = 32 << 20
)

type readAhead struct {
	mu      sync.Mutex
	order   []lazyfs.BaseEntry
	index   map[string]int
	last    int
	start   int
	lo, hi  int
	run     int
	aheadLo int
	aheadHi int
	window  int
	fetcher bool
}

func (a *readAhead) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.order, a.index = nil, nil
	a.restart(-1)
}

func (a *readAhead) restart(i int) {
	a.last, a.start, a.lo, a.hi, a.run = i, i, i, i, 0
	a.aheadLo, a.aheadHi, a.window = i, i, readAheadFirstWindow
}

// WalkOrder lists the base's files as a depth-first walk of sorted directories.
func WalkOrder(m *lazyfs.Manifest) []lazyfs.BaseEntry {
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

func (f *Fetcher) observeRead(p string) {
	if os.Getenv("GS_READAHEAD") == "off" {
		return
	}
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
		a.order = WalkOrder(manifest)
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
		a.restart(i)
		return
	}
	a.last = i
	a.lo, a.hi = min(a.lo, i), max(a.hi, i)
	a.run++
	if a.run < readAheadTrigger || a.fetcher {
		return
	}
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

func (f *Fetcher) readAheadFetch(entries []lazyfs.BaseEntry) {
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
	f.mu.Unlock()

	seen := map[string]bool{}
	uncached := make([]lazyfs.BaseEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Size > readAheadMaxFile || seen[entry.ContentHash] {
			continue
		}
		seen[entry.ContentHash] = true
		if f.cache.Exists(entry.ContentHash) {
			continue
		}
		uncached = append(uncached, entry)
	}
	if len(uncached) == 0 {
		return
	}

	f.mu.Lock()
	if time.Until(f.downUntil) > 0 || f.commitID != commitID {
		f.mu.Unlock()
		return
	}
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
	for _, entry := range uncached {
		if f.inflight[entry.ContentHash] != nil || f.completed[entry.ContentHash] {
			continue
		}
		if total+entry.Size > readAheadMaxBytes {
			break
		}
		if len(cur) >= BatchMaxFiles || curBytes+entry.Size > BatchMaxBytes {
			flush()
		}
		cur = append(cur, entry)
		curBytes += entry.Size
		total += entry.Size
	}
	flush()
	f.mu.Unlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, FetchConcurrency)
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
