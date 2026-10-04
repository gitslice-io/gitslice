package gitcompat

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestLazyBuildScale measures how the lazy builder grows with history. It is
// skipped unless GITSLICE_SCALE_COMMITS is set, e.g. GITSLICE_SCALE_COMMITS=20000.
// Each commit changes a few files in a tree of many directories, like a monorepo.
func TestLazyBuildScale(t *testing.T) {
	commits, _ := strconv.Atoi(os.Getenv("GITSLICE_SCALE_COMMITS"))
	if commits == 0 {
		t.Skip("set GITSLICE_SCALE_COMMITS to measure")
	}
	ctx := context.Background()
	mem := memory.New()
	repo := filepath.Join(t.TempDir(), "acme", "big.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjectedRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	p := &Projector{blobs: mem.Blobs, lazy: true}
	state := &projectionState{Version: projectionVersion, Account: "acme", Slice: "big", Files: map[string]projectedFile{}}
	files := map[string]projectedFile{}
	known := map[string]string{}
	var pending []pendingCommit
	var need []string
	ids := map[string]string{}
	next := 0
	for i := 0; i < commits; i++ {
		var changes []fileChange
		for j := 0; j < 5; j++ {
			var path string
			if i%3 == 0 || next < 50 {
				path = fmt.Sprintf("acme/big/svc%02d/pkg%03d/dir%02d/file%06d.go", next%40, next%300, next%20, next)
				next++
			} else {
				path = fmt.Sprintf("acme/big/svc%02d/pkg%03d/dir%02d/file%06d.go", (next-1-j)%40, (next-1-j)%300, (next-1-j)%20, next-1-j)
			}
			content := []byte(fmt.Sprintf("version %d of %s\n", i, path))
			hash := objectid.RawContentHash(content)
			ids[hash] = objectid.GitBlobID(content)
			changes = append(changes, fileChange{Path: path, Mode: "100644", ContentHash: hash, Size: int64(len(content))})
			files[path] = projectedFile{Mode: "100644", ContentHash: hash}
			need = append(need, hash)
		}
		pending = append(pending, pendingCommit{native: &corev1.Commit{Id: "sha256:" + strconv.Itoa(i), Author: "u", CreatedAt: "2026-10-01T00:00:00Z", Message: "commit " + strconv.Itoa(i)}, changes: changes})
	}
	if err := mem.Blobs.SetGitBlobIDs(ctx, ids); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	started := time.Now()
	if _, err := p.appendLazy(ctx, repo, state, files, pending, map[string]string{}, known, need); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	size := dirSize(filepath.Join(repo, "objects"))
	t.Logf("%d commits, %d files: %v (%.2f ms per commit); repository objects %.1f MB; peak heap about %.0f MB; state file %.1f MB",
		commits, len(files), elapsed.Round(time.Millisecond), float64(elapsed.Microseconds())/1000/float64(commits), float64(size)/1e6, float64(after.HeapSys)/1e6, fileSize(filepath.Join(repo, projectionStateFile))/1e6)
	if head := gitIn(t, repo, nil, "rev-parse", projectedBranch); head != state.GitHead {
		t.Fatalf("head %s, state %s", head, state.GitHead)
	}
	if got := gitIn(t, repo, nil, "rev-list", "--count", projectedBranch); got != strconv.Itoa(commits) {
		t.Fatalf("%s commits, want %d", got, commits)
	}
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func fileSize(path string) float64 {
	if info, err := os.Stat(path); err == nil {
		return float64(info.Size())
	}
	return 0
}

// TestHydrateScale measures writing file contents into a lazily built
// repository. It is skipped unless GITSLICE_SCALE_FILES is set; each file is
// GITSLICE_SCALE_FILE_KB kilobytes (default 40) of incompressible bytes.
func TestHydrateScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("GITSLICE_SCALE_FILES"))
	if n == 0 {
		t.Skip("set GITSLICE_SCALE_FILES to measure")
	}
	kb, _ := strconv.Atoi(os.Getenv("GITSLICE_SCALE_FILE_KB"))
	if kb == 0 {
		kb = 40
	}
	ctx := context.Background()
	root := t.TempDir()
	store, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	mem := memory.New()
	p := &Projector{blobs: mem.Blobs, objectStore: store, lazy: true}
	repo := filepath.Join(t.TempDir(), "acme", "h.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjectedRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	ids := map[string]string{}
	var pending []pendingCommit
	var need []string
	var total int64
	for i := 0; i < n; i++ {
		data := make([]byte, kb*1024)
		rng.Read(data)
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
		total += int64(len(data))
		path := fmt.Sprintf("acme/h/d%03d/f%06d.bin", i%200, i)
		pending = append(pending, pendingCommit{
			native:  &corev1.Commit{Id: "sha256:" + strconv.Itoa(i), Author: "u", CreatedAt: "2026-10-01T00:00:00Z", Message: "add"},
			changes: []fileChange{{Path: path, Mode: "100644", ContentHash: hash, Size: int64(len(data))}},
		})
	}
	if err := mem.Blobs.SetGitBlobIDs(ctx, ids); err != nil {
		t.Fatal(err)
	}
	state := &projectionState{Version: projectionVersion, Account: "acme", Slice: "h", Files: map[string]projectedFile{}}
	if _, err := p.appendLazy(ctx, repo, state, map[string]projectedFile{}, pending, map[string]string{}, map[string]string{}, need); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	started := time.Now()
	if err := p.HydrateFor(ctx, repo, []byte(pkt("command=fetch\n", "DELIM", "want "+state.GitHead+"\n", "done\n", "FLUSH"))); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	t.Logf("hydrated %d files, %.0f MB, in %v (%.0f MB/s); peak heap about %.0f MB", n, float64(total)/1e6, elapsed.Round(time.Millisecond), float64(total)/1e6/elapsed.Seconds(), float64(after.HeapSys)/1e6)
	if got := gitIn(t, repo, nil, "rev-list", "--objects", "--missing=print", state.GitHead); strings.Contains(got, "?") {
		t.Fatal("files are still missing")
	}
}
