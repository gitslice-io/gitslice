package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/objectid"
)

func TestScanWorkspaceFilesIncludesUserDotPaths(t *testing.T) {
	workspace := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("acme/payment/.env", "APP_ENV=test\n")
	write("acme/payment/.github/workflows/ci.yml", "name: ci\n")
	write("acme/payment/src/.keep", "")
	write(".gs/slice.json", "{}")
	write(".gs/state.json", "{}")
	write(".git/config", "[core]\n")
	write(".gitslice/cache", "local\n")

	r := Runner{Home: t.TempDir(), Dir: workspace}
	files, err := r.scanWorkspaceFiles(WorkspaceConfig{
		Account:       "acme",
		Slice:         "payment",
		IncludedPaths: []string{"/acme/payment"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"/acme/payment/.env",
		"/acme/payment/.github/workflows/ci.yml",
		"/acme/payment/src/.keep",
	} {
		if _, ok := files[want]; !ok {
			t.Fatalf("scan missing %s; got paths %#v", want, files)
		}
	}
	for _, notWant := range []string{
		"/acme/payment/.gs/state.json",
		"/acme/payment/.git/config",
		"/acme/payment/.gitslice/cache",
	} {
		if _, ok := files[notWant]; ok {
			t.Fatalf("scan included workspace metadata path %s: %#v", notWant, files[notWant])
		}
	}
}

// TestScanWorkspaceFilesReusesStatCache: a scan uses a file's recorded hash
// while its stat matches, and reads the file again when it changed, when it
// was modified no earlier than the record, or when its content left the
// client cache.
func TestScanWorkspaceFilesReusesStatCache(t *testing.T) {
	workspace := t.TempDir()
	r := Runner{Home: t.TempDir(), Dir: workspace}
	ws := WorkspaceConfig{Account: "acme", Slice: "payment", IncludedPaths: []string{"/acme/payment"}}
	write := func(rel, content string, modTime time.Time) {
		t.Helper()
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	hashOf := func(content string) string { return objectid.RawContentHash([]byte(content)) }
	scanned := func(globalPath string) string {
		t.Helper()
		files, err := r.scanWorkspaceFiles(ws)
		if err != nil {
			t.Fatal(err)
		}
		return files[globalPath].ContentHash
	}
	// recordAs replaces a file's recorded hash, keeping its recorded stat.
	recordAs := func(rel, contentHash string) {
		t.Helper()
		cache := readStatCache(workspace)
		entry, ok := cache.Files[rel]
		if !ok {
			t.Fatalf("no record for %s", rel)
		}
		entry.ContentHash = contentHash
		cache.Files[rel] = entry
		writeStatCache(workspace, cache.Files)
	}

	past := time.Now().Add(-time.Hour)
	write(".gs/slice.json", "{}", past)
	write("acme/payment/a.txt", "alpha\n", past)
	write("acme/payment/b.txt", "bravo\n", past)
	if got := scanned("/acme/payment/a.txt"); got != hashOf("alpha\n") {
		t.Fatalf("first scan hash = %s", got)
	}

	// The record is used: pointed at other cached content, the scan
	// believes it rather than reading the file.
	recordAs("acme/payment/a.txt", hashOf("bravo\n"))
	if got := scanned("/acme/payment/a.txt"); got != hashOf("bravo\n") {
		t.Fatalf("a matching record was not used: %s", got)
	}

	// A changed file (same size, new modification time) is read again.
	write("acme/payment/a.txt", "ALPHA\n", past.Add(time.Minute))
	if got := scanned("/acme/payment/a.txt"); got != hashOf("ALPHA\n") {
		t.Fatalf("a changed file kept its old hash: %s", got)
	}

	// A record whose content is not in the client cache is not used.
	recordAs("acme/payment/b.txt", hashOf("never cached\n"))
	if got := scanned("/acme/payment/b.txt"); got != hashOf("bravo\n") {
		t.Fatalf("a record without cached content was used: %s", got)
	}

	// A file modified no earlier than the record is read again, even when
	// its stat matches: a change in the same clock tick would not show.
	future := time.Now().Add(time.Hour)
	write("acme/payment/b.txt", "bravo\n", future)
	cache := readStatCache(workspace)
	cache.Files["acme/payment/b.txt"] = statCacheEntry{Size: 6, ModTimeNs: future.UnixNano(), Mode: 0o100644, ContentHash: hashOf("ALPHA\n")}
	writeStatCache(workspace, cache.Files)
	if got := scanned("/acme/payment/b.txt"); got != hashOf("bravo\n") {
		t.Fatalf("a record newer than the cache was used: %s", got)
	}
}

func TestHashWorkspaceFilesUsesBoundedConcurrency(t *testing.T) {
	paths := []string{"a", "b", "c", "d", "e", "f"}
	started := make(chan struct{}, len(paths))
	release := make(chan struct{})
	var mu sync.Mutex
	current := 0
	maximum := 0

	done := make(chan error, 1)
	go func() {
		objects, err := hashWorkspaceFiles(context.Background(), paths, 3, func(p string) (clientcache.Object, error) {
			mu.Lock()
			current++
			if current > maximum {
				maximum = current
			}
			mu.Unlock()
			started <- struct{}{}
			<-release
			mu.Lock()
			current--
			mu.Unlock()
			return clientcache.Object{ContentHash: objectid.RawContentHash([]byte(p)), Size: int64(len(p))}, nil
		})
		if err == nil && len(objects) != len(paths) {
			err = fmt.Errorf("hashed object count = %d, want %d", len(objects), len(paths))
		}
		done <- err
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for hash worker")
		}
	}
	mu.Lock()
	gotCurrent, gotMaximum := current, maximum
	mu.Unlock()
	if gotCurrent != 3 || gotMaximum != 3 {
		t.Fatalf("hash concurrency current=%d max=%d, want 3", gotCurrent, gotMaximum)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hash workers to finish")
	}
}

func TestShouldSkipAgentMetadataOnlyAtWorkspaceRoot(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{rel: ".agent-meta.json", want: true},
		{rel: "sub/.agent-meta.json", want: false},
		{rel: ".agent-meta-abc123.tmp", want: true},
		{rel: ".git/x", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			if got := shouldSkip(tt.rel, nil); got != tt.want {
				t.Fatalf("shouldSkip(%q) = %t, want %t", tt.rel, got, tt.want)
			}
		})
	}
}
